package meme

import (
	"context"
	"fmt"
	"log/slog"
	"mime"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5"

	"github.com/nyxsr/meme-portal/api/internal/config"
	"github.com/nyxsr/meme-portal/api/internal/platform/token"
)

// Gate memeriksa apakah user berhak membuat meme (langganan aktif & kuota).
// Diimplementasikan oleh modul subscription lewat structural typing.
type Gate interface {
	HasActiveSubscription(ctx context.Context, userID string) error
	CheckQuotaTx(ctx context.Context, tx pgx.Tx, userID string) error
}

type Service struct {
	repo    *Repository
	s3      *s3.Client
	bucket  string
	baseURL string // public base URL Garage web endpoint
	gate    Gate
}

func NewService(repo *Repository, s3Client *s3.Client, cfg config.StorageConfig, gate Gate) *Service {
	return &Service{repo: repo, s3: s3Client, bucket: cfg.Bucket, baseURL: cfg.PublicBaseURL, gate: gate}
}

// PrepareUpload membuat record meme (pending) dan mengembalikan presigned PUT URL
// untuk mengunggah gambar dasar langsung ke S3/Garage.
func (s *Service) PrepareUpload(ctx context.Context, userID, title, ext, contentType string, fileSize int64) (UploadResponse, error) {
	normExt, normMIME, err := validateImageUpload(ext, contentType, fileSize)
	if err != nil {
		return UploadResponse{}, err
	}
	objectKey := fmt.Sprintf("base/%s%s", token.RandomHex(12), normExt)

	m, err := s.repo.CreateWithCheck(ctx, Meme{
		UserID: userID, Title: title,
		BaseImageURL: s.publicURL(objectKey),
	}, func(ctx context.Context, tx pgx.Tx) error {
		return s.gate.CheckQuotaTx(ctx, tx, userID)
	})
	if err != nil {
		return UploadResponse{}, err
	}

	presign := s3.NewPresignClient(s.s3)
	input := &s3.PutObjectInput{
		Bucket:        &s.bucket,
		Key:           &objectKey,
		ContentType:   aws.String(normMIME),
		ContentLength: aws.Int64(fileSize),
	}
	req, err := presign.PresignPutObject(ctx, input, s3.WithPresignExpires(maxPresignTTL))
	if err != nil {
		return UploadResponse{}, fmt.Errorf("meme service: presign gagal: %w", err)
	}
	return UploadResponse{ID: m.ID, UploadURL: req.URL, BaseURL: m.BaseImageURL}, nil
}

// ResultUpload mengembalikan presigned PUT URL untuk gambar hasil render
// (canvas) milik meme <id>; complete kemudian menyimpan URL publiknya.
func (s *Service) ResultUpload(ctx context.Context, userID, id string, fileSize int64) (UploadResponse, error) {
	if err := s.gate.HasActiveSubscription(ctx, userID); err != nil {
		return UploadResponse{}, err
	}
	if err := validateFileSize(fileSize); err != nil {
		return UploadResponse{}, err
	}
	if _, err := s.repo.GetByID(ctx, id, userID); err != nil {
		return UploadResponse{}, err
	}
	objectKey := fmt.Sprintf("result/%s.png", id)

	presign := s3.NewPresignClient(s.s3)
	req, err := presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:        &s.bucket,
		Key:           &objectKey,
		ContentType:   aws.String("image/png"),
		ContentLength: aws.Int64(fileSize),
	}, s3.WithPresignExpires(maxPresignTTL))
	if err != nil {
		return UploadResponse{}, fmt.Errorf("meme service: presign gagal: %w", err)
	}
	return UploadResponse{ID: id, UploadURL: req.URL, BaseURL: s.publicURL(objectKey)}, nil
}

func (s *Service) Complete(ctx context.Context, userID, id, resultURL string) (Meme, error) {
	if err := s.gate.HasActiveSubscription(ctx, userID); err != nil {
		return Meme{}, err
	}
	return s.repo.SetResultURL(ctx, id, userID, resultURL)
}

func (s *Service) List(ctx context.Context, userID string, limit, offset int32) (ListResponse, error) {
	items, total, err := s.repo.ListByUser(ctx, userID, limit, offset)
	if err != nil {
		return ListResponse{}, err
	}
	if items == nil {
		items = []Meme{}
	}
	return ListResponse{Items: items, Total: total}, nil
}

func (s *Service) Delete(ctx context.Context, userID, id string) error {
	deleted, err := s.repo.Delete(ctx, id, userID)
	if err != nil {
		return err
	}
	if s.s3 == nil {
		return nil
	}
	urls := []string{deleted.BaseImageURL}
	if deleted.ResultURL != nil {
		urls = append(urls, *deleted.ResultURL)
	}
	for _, rawURL := range urls {
		key, ok := s.objectKeyFromURL(rawURL)
		if !ok {
			continue
		}
		if _, err := s.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: &s.bucket,
			Key:    &key,
		}); err != nil {
			slog.Warn("meme service: s3 delete gagal", "meme_id", id, "key", key, "err", err)
		}
	}
	return nil
}

func (s *Service) objectKeyFromURL(rawURL string) (string, bool) {
	if rawURL == "" {
		return "", false
	}
	var rel string
	if s.baseURL != "" {
		prefix := strings.TrimRight(s.baseURL, "/") + "/"
		if !strings.HasPrefix(rawURL, prefix) {
			return "", false
		}
		rel = strings.TrimPrefix(rawURL, prefix)
	} else {
		rel = rawURL
	}
	cleaned := path.Clean(strings.TrimLeft(rel, "/"))
	if cleaned == "." || cleaned == "" || strings.HasPrefix(cleaned, "..") {
		return "", false
	}
	if s.baseURL == "" && !strings.HasPrefix(cleaned, "base/") && !strings.HasPrefix(cleaned, "result/") {
		return "", false
	}
	return cleaned, true
}

func (s *Service) publicURL(key string) string {
	if s.baseURL == "" {
		return key
	}
	return strings.TrimRight(s.baseURL, "/") + "/" + path.Clean(key)
}

var extToMIME = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".webp": "image/webp",
}

func validateFileSize(fileSize int64) error {
	if fileSize <= 0 {
		return ErrInvalidFile
	}
	if fileSize > maxUploadBytes {
		return ErrFileTooLarge
	}
	return nil
}

func validateImageUpload(ext, contentType string, fileSize int64) (normExt, normMIME string, err error) {
	normExt = strings.ToLower(strings.TrimSpace(ext))
	if !allowedExt(normExt) {
		return "", "", ErrInvalidFile
	}
	expectedMIME, ok := extToMIME[normExt]
	if !ok {
		return "", "", ErrInvalidFile
	}
	mediaType, _, parseErr := mime.ParseMediaType(contentType)
	if parseErr != nil || strings.ToLower(strings.TrimSpace(mediaType)) != expectedMIME {
		return "", "", ErrInvalidFile
	}
	if err := validateFileSize(fileSize); err != nil {
		return "", "", err
	}
	return normExt, expectedMIME, nil
}

func allowedExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp":
		return true
	}
	return false
}
