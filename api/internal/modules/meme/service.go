package meme

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"mime"
	"net/url"
	"path"
	"strings"
	"time"

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
	repo         *Repository
	s3           *s3.Client
	bucket       string
	baseURL      string // public base URL Garage web endpoint
	gate         Gate
	uploadSecret []byte
}

func NewService(repo *Repository, s3Client *s3.Client, cfg config.StorageConfig, gate Gate, uploadSecret string) *Service {
	secret := uploadSecret
	if secret == "" {
		secret = cfg.SecretKey
	}
	if secret == "" {
		secret = "meme-upload-fallback"
	}
	return &Service{
		repo:         repo,
		s3:           s3Client,
		bucket:       cfg.Bucket,
		baseURL:      cfg.PublicBaseURL,
		gate:         gate,
		uploadSecret: []byte(secret),
	}
}

// PrepareUpload membuat record meme (pending) dan mengembalikan signed upload URL
// yang menegakkan batas ukuran & MIME sebelum meneruskan objek ke S3/Garage.
func (s *Service) PrepareUpload(ctx context.Context, userID, title, ext, contentType string, fileSize *int64, uploadBaseURL string) (UploadResponse, error) {
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

	var exactSize int64
	if fileSize != nil {
		exactSize = *fileSize
	}
	uploadURL, err := s.buildUploadURL(uploadBaseURL, uploadTokenClaims{
		Key:      objectKey,
		MIME:     normMIME,
		MaxBytes: maxUploadBytes,
		Size:     exactSize,
		Exp:      time.Now().Add(maxPresignTTL).Unix(),
	})
	if err != nil {
		return UploadResponse{}, fmt.Errorf("meme service: sign upload url gagal: %w", err)
	}
	return UploadResponse{ID: m.ID, UploadURL: uploadURL, BaseURL: m.BaseImageURL}, nil
}

// ResultUpload mengembalikan signed upload URL untuk gambar hasil render
// (canvas) milik meme <id>; complete kemudian menyimpan URL publiknya.
func (s *Service) ResultUpload(ctx context.Context, userID, id string, fileSize *int64, uploadBaseURL string) (UploadResponse, error) {
	if err := s.gate.HasActiveSubscription(ctx, userID); err != nil {
		return UploadResponse{}, err
	}
	if err := validateOptionalFileSize(fileSize); err != nil {
		return UploadResponse{}, err
	}
	if _, err := s.repo.GetByID(ctx, id, userID); err != nil {
		return UploadResponse{}, err
	}
	objectKey := fmt.Sprintf("result/%s.png", id)

	var exactSize int64
	if fileSize != nil {
		exactSize = *fileSize
	}
	uploadURL, err := s.buildUploadURL(uploadBaseURL, uploadTokenClaims{
		Key:      objectKey,
		MIME:     "image/png",
		MaxBytes: maxUploadBytes,
		Size:     exactSize,
		Exp:      time.Now().Add(maxPresignTTL).Unix(),
	})
	if err != nil {
		return UploadResponse{}, fmt.Errorf("meme service: sign upload url gagal: %w", err)
	}
	return UploadResponse{ID: id, UploadURL: uploadURL, BaseURL: s.publicURL(objectKey)}, nil
}

func (s *Service) buildUploadURL(uploadBaseURL string, claims uploadTokenClaims) (string, error) {
	tok, err := signUploadToken(s.uploadSecret, claims)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/api/v1/memes/upload-object?token=%s", strings.TrimRight(uploadBaseURL, "/"), url.QueryEscape(tok)), nil
}

func (s *Service) VerifyUploadToken(raw string) (uploadTokenClaims, error) {
	return verifyUploadToken(s.uploadSecret, raw, time.Now())
}

func (s *Service) PutObject(ctx context.Context, key, contentType string, data []byte) error {
	_, err := s.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        &s.bucket,
		Key:           &key,
		Body:          bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))),
		ContentType:   aws.String(contentType),
	})
	if err != nil {
		return fmt.Errorf("meme service: s3 put object gagal: %w", err)
	}
	return nil
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

func validateOptionalFileSize(fileSize *int64) error {
	if fileSize == nil {
		return nil
	}
	if *fileSize <= 0 {
		return ErrInvalidFile
	}
	if *fileSize > maxUploadBytes {
		return ErrFileTooLarge
	}
	return nil
}

func validateImageUpload(ext, contentType string, fileSize *int64) (normExt, normMIME string, err error) {
	normExt = strings.ToLower(strings.TrimSpace(ext))
	if !allowedExt(normExt) {
		return "", "", ErrInvalidFile
	}
	expectedMIME, ok := extToMIME[normExt]
	if !ok {
		return "", "", ErrInvalidFile
	}
	if strings.TrimSpace(contentType) != "" {
		mediaType, _, parseErr := mime.ParseMediaType(contentType)
		if parseErr != nil || strings.ToLower(strings.TrimSpace(mediaType)) != expectedMIME {
			return "", "", ErrInvalidFile
		}
	}
	if err := validateOptionalFileSize(fileSize); err != nil {
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
