package meme_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5"

	"github.com/nyxsr/meme-portal/api/internal/config"
	"github.com/nyxsr/meme-portal/api/internal/modules/meme"
)

type stubGate struct{}

func (stubGate) HasActiveSubscription(context.Context, string) error   { return nil }
func (stubGate) CheckQuotaTx(context.Context, pgx.Tx, string) error { return nil }

func newStubS3Client(t *testing.T, serverURL string) *s3.Client {
	t.Helper()
	cfg := aws.Config{
		Region:      "garage",
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
	}
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(serverURL)
		o.UsePathStyle = true
		o.RetryMaxAttempts = 1
	})
}

func TestService_Delete_BestEffortS3Cleanup(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	repo := meme.NewPostgresRepository(pool)

	userA := seedTestUser(t, pool, "userA")
	userB := seedTestUser(t, pool, "userB")

	var (
		mu          sync.Mutex
		deletePaths []string
		failS3      bool
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if r.Method == http.MethodDelete {
			deletePaths = append(deletePaths, r.URL.Path)
		}
		shouldFail := failS3
		mu.Unlock()
		if shouldFail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	baseURL := "http://localhost:3902/meme-portal"
	svc := meme.NewService(repo, newStubS3Client(t, srv.URL), config.StorageConfig{
		Bucket:        "meme-portal",
		PublicBaseURL: baseURL,
	}, stubGate{})

	// 1. Owned meme with both base and result URLs -> sends S3 DELETE for both keys.
	m1, err := repo.Create(ctx, meme.Meme{
		UserID:       userA,
		Title:        "With Result",
		BaseImageURL: baseURL + "/base/abc123.png",
	})
	if err != nil {
		t.Fatalf("create m1: %v", err)
	}
	if _, err := repo.SetResultURL(ctx, m1.ID, userA, baseURL+"/result/"+m1.ID+".png"); err != nil {
		t.Fatalf("set result m1: %v", err)
	}

	// Unauthorized delete sends zero S3 requests.
	if err := svc.Delete(ctx, userB, m1.ID); !errors.Is(err, meme.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for unauthorized delete, got %v", err)
	}
	mu.Lock()
	if len(deletePaths) != 0 {
		t.Fatalf("expected 0 S3 deletes on unauthorized attempt, got %v", deletePaths)
	}
	mu.Unlock()

	if err := svc.Delete(ctx, userA, m1.ID); err != nil {
		t.Fatalf("owner delete m1 failed: %v", err)
	}
	mu.Lock()
	if len(deletePaths) != 2 {
		t.Fatalf("expected 2 S3 deletes (base + result), got %v", deletePaths)
	}
	deletePaths = nil
	mu.Unlock()

	// 2. External/non-matching resultUrl is skipped, and S3 500 does not fail Delete.
	m2, err := repo.Create(ctx, meme.Meme{
		UserID:       userA,
		Title:        "External Result",
		BaseImageURL: baseURL + "/base/def456.png",
	})
	if err != nil {
		t.Fatalf("create m2: %v", err)
	}
	if _, err := repo.SetResultURL(ctx, m2.ID, userA, "http://evil.example/hijacked.png"); err != nil {
		t.Fatalf("set result m2: %v", err)
	}

	mu.Lock()
	failS3 = true
	mu.Unlock()

	if err := svc.Delete(ctx, userA, m2.ID); err != nil {
		t.Fatalf("expected Delete to succeed despite S3 500, got %v", err)
	}
	mu.Lock()
	if len(deletePaths) != 1 {
		t.Fatalf("expected 1 S3 delete (only base, external result skipped), got %v", deletePaths)
	}
	mu.Unlock()
}

func TestService_PrepareUpload_Validation(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	repo := meme.NewPostgresRepository(pool)
	userID := seedTestUser(t, pool, "prepare-validation")

	svc := meme.NewService(repo, newStubS3Client(t, "http://localhost:3900"), config.StorageConfig{
		Bucket:        "meme-portal",
		PublicBaseURL: "http://localhost:3902/meme-portal",
	}, stubGate{})

	const maxBytes int64 = 5 << 20
	sizePtr := func(v int64) *int64 { return &v }

	invalidCases := []struct {
		name        string
		ext         string
		contentType string
		fileSize    *int64
		wantErr     error
	}{
		{"html ext", ".html", "text/html", sizePtr(64), meme.ErrInvalidFile},
		{"html ext omitted size and mime", ".html", "", nil, meme.ErrInvalidFile},
		{"exe ext", ".exe", "application/octet-stream", sizePtr(1024), meme.ErrInvalidFile},
		{"zip ext", ".zip", "application/zip", sizePtr(1024), meme.ErrInvalidFile},
		{"svg ext", ".svg", "image/svg+xml", sizePtr(512), meme.ErrInvalidFile},
		{"empty ext", "", "image/png", sizePtr(512), meme.ErrInvalidFile},
		{"png ext with text/html", ".png", "text/html", sizePtr(512), meme.ErrInvalidFile},
		{"png ext with text/html omitted size", ".png", "text/html", nil, meme.ErrInvalidFile},
		{"jpg ext with image/png", ".jpg", "image/png", sizePtr(512), meme.ErrInvalidFile},
		{"explicit zero fileSize", ".png", "image/png", sizePtr(0), meme.ErrInvalidFile},
		{"negative fileSize", ".png", "image/png", sizePtr(-1), meme.ErrInvalidFile},
		{"oversized fileSize", ".png", "image/png", sizePtr(maxBytes + 1), meme.ErrFileTooLarge},
	}

	for _, tc := range invalidCases {
		t.Run("reject/"+tc.name, func(t *testing.T) {
			_, err := svc.PrepareUpload(ctx, userID, tc.name, tc.ext, tc.contentType, tc.fileSize)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("PrepareUpload(%q, %q, %v) err = %v, want %v", tc.ext, tc.contentType, tc.fileSize, err, tc.wantErr)
			}
		})
	}

	validCases := []struct {
		name            string
		ext             string
		contentType     string
		fileSize        *int64
		wantSignedHdrs  []string
	}{
		{"jpg", ".jpg", "image/jpeg", sizePtr(1024), []string{"content-length", "content-type"}},
		{"uppercase JPEG + MIME", ".JPEG", "IMAGE/JPEG", sizePtr(maxBytes), []string{"content-length", "content-type"}},
		{"png with params", ".png", "image/png; charset=binary", sizePtr(maxBytes), []string{"content-length", "content-type"}},
		{"gif", ".gif", "image/gif", sizePtr(2048), []string{"content-length", "content-type"}},
		{"webp", ".webp", "image/webp", sizePtr(4096), []string{"content-length", "content-type"}},
		{"omitted fileSize", ".png", "image/png", nil, []string{"content-type"}},
		{"omitted contentType and fileSize", ".png", "", nil, []string{"content-type"}},
	}

	for _, tc := range validCases {
		t.Run("accept/"+tc.name, func(t *testing.T) {
			res, err := svc.PrepareUpload(ctx, userID, tc.name, tc.ext, tc.contentType, tc.fileSize)
			if err != nil {
				t.Fatalf("PrepareUpload(%q, %q, %v) unexpected err: %v", tc.ext, tc.contentType, tc.fileSize, err)
			}
			assertSignedHeaders(t, res.UploadURL, tc.wantSignedHdrs...)
		})
	}
}

func TestService_ResultUpload_Validation(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	repo := meme.NewPostgresRepository(pool)
	userID := seedTestUser(t, pool, "result-validation")

	svc := meme.NewService(repo, newStubS3Client(t, "http://localhost:3900"), config.StorageConfig{
		Bucket:        "meme-portal",
		PublicBaseURL: "http://localhost:3902/meme-portal",
	}, stubGate{})

	const maxBytes int64 = 5 << 20
	sizePtr := func(v int64) *int64 { return &v }

	m, err := repo.Create(ctx, meme.Meme{
		UserID:       userID,
		Title:        "Owned",
		BaseImageURL: "http://localhost:3902/meme-portal/base/abc.png",
	})
	if err != nil {
		t.Fatalf("create meme: %v", err)
	}

	if _, err := svc.ResultUpload(ctx, userID, m.ID, sizePtr(0)); !errors.Is(err, meme.ErrInvalidFile) {
		t.Fatalf("expected ErrInvalidFile for size 0, got %v", err)
	}
	if _, err := svc.ResultUpload(ctx, userID, m.ID, sizePtr(-5)); !errors.Is(err, meme.ErrInvalidFile) {
		t.Fatalf("expected ErrInvalidFile for size -5, got %v", err)
	}
	if _, err := svc.ResultUpload(ctx, userID, m.ID, sizePtr(maxBytes+1)); !errors.Is(err, meme.ErrFileTooLarge) {
		t.Fatalf("expected ErrFileTooLarge for size maxBytes+1, got %v", err)
	}

	resCompat, err := svc.ResultUpload(ctx, userID, m.ID, nil)
	if err != nil {
		t.Fatalf("ResultUpload with nil size failed: %v", err)
	}
	assertSignedHeaders(t, resCompat.UploadURL, "content-type")

	res, err := svc.ResultUpload(ctx, userID, m.ID, sizePtr(maxBytes))
	if err != nil {
		t.Fatalf("ResultUpload at boundary failed: %v", err)
	}
	assertSignedHeaders(t, res.UploadURL, "content-length", "content-type")
}
