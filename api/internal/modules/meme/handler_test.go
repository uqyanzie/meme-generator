package meme_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nyxsr/meme-portal/api/internal/config"
	"github.com/nyxsr/meme-portal/api/internal/middleware"
	"github.com/nyxsr/meme-portal/api/internal/modules/meme"
	"github.com/nyxsr/meme-portal/api/internal/platform/token"
	"github.com/nyxsr/meme-portal/api/internal/response"
)

const testMaxUploadBytes int64 = 5 << 20

type apiEnvelope struct {
	Success bool            `json:"success"`
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func assertSignedHeaders(t *testing.T, rawURL string, want ...string) {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse presigned url %q: %v", rawURL, err)
	}
	signed := strings.Split(parsed.Query().Get("X-Amz-SignedHeaders"), ";")
	set := make(map[string]bool, len(signed))
	for _, h := range signed {
		set[strings.ToLower(strings.TrimSpace(h))] = true
	}
	for _, w := range want {
		if !set[w] {
			t.Errorf("expected X-Amz-SignedHeaders (%q) to contain %q", parsed.Query().Get("X-Amz-SignedHeaders"), w)
		}
	}
}

func TestHandler_UploadErrorStatusMapping(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	userID := seedTestUser(t, pool, "handler-upload")

	tokens := token.NewManager("test-secret", "meme-portal", time.Hour, time.Hour, nil)
	pair, err := tokens.Issue(ctx, userID, "user")
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	repo := meme.NewPostgresRepository(pool)
	svc := meme.NewService(repo, newStubS3Client(t, "http://localhost:3900"), config.StorageConfig{
		Bucket:        "meme-portal",
		PublicBaseURL: "http://localhost:3902/meme-portal",
	}, stubGate{})
	h := meme.NewHandler(svc)

	mux := http.NewServeMux()
	meme.RegisterRoutes(mux, h, middleware.Auth(tokens))

	doJSON := func(method, path string, body any) (*httptest.ResponseRecorder, apiEnvelope) {
		t.Helper()
		var buf bytes.Buffer
		if body != nil {
			switch v := body.(type) {
			case string:
				buf.WriteString(v)
			default:
				if err := json.NewEncoder(&buf).Encode(v); err != nil {
					t.Fatalf("encode body: %v", err)
				}
			}
		}
		req := httptest.NewRequest(method, path, &buf)
		req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		var env apiEnvelope
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode response %q: %v", rec.Body.String(), err)
		}
		return rec, env
	}

	// 1. PrepareUpload rejections: disallowed extensions, MIME spoofing, invalid/oversized fileSize.
	rejectCases := []struct {
		name       string
		payload    map[string]any
		wantStatus int
		wantCode   int
	}{
		{
			name:       "html extension and mime",
			payload:    map[string]any{"title": "xss", "filename": "payload.html", "contentType": "text/html", "fileSize": 64},
			wantStatus: http.StatusBadRequest,
			wantCode:   response.CodeInvalidImage,
		},
		{
			name:       "exe extension",
			payload:    map[string]any{"title": "bin", "filename": "malware.exe", "contentType": "application/octet-stream", "fileSize": 1024},
			wantStatus: http.StatusBadRequest,
			wantCode:   response.CodeInvalidImage,
		},
		{
			name:       "zip extension",
			payload:    map[string]any{"title": "zip", "filename": "GTA.V.Full.Installer.zip", "contentType": "application/zip", "fileSize": 1024},
			wantStatus: http.StatusBadRequest,
			wantCode:   response.CodeInvalidImage,
		},
		{
			name:       "svg extension excluded",
			payload:    map[string]any{"title": "svg", "filename": "vector.svg", "contentType": "image/svg+xml", "fileSize": 512},
			wantStatus: http.StatusBadRequest,
			wantCode:   response.CodeInvalidImage,
		},
		{
			name:       "mime spoofing png with text/html",
			payload:    map[string]any{"title": "spoof", "filename": "spoof.png", "contentType": "text/html", "fileSize": 512},
			wantStatus: http.StatusBadRequest,
			wantCode:   response.CodeInvalidImage,
		},
		{
			name:       "missing fileSize (0)",
			payload:    map[string]any{"title": "zero", "filename": "ok.png", "contentType": "image/png"},
			wantStatus: http.StatusBadRequest,
			wantCode:   response.CodeInvalidImage,
		},
		{
			name:       "negative fileSize",
			payload:    map[string]any{"title": "neg", "filename": "ok.png", "contentType": "image/png", "fileSize": -10},
			wantStatus: http.StatusBadRequest,
			wantCode:   response.CodeInvalidImage,
		},
		{
			name:       "oversized fileSize (5 MiB + 1)",
			payload:    map[string]any{"title": "huge", "filename": "huge.png", "contentType": "image/png", "fileSize": testMaxUploadBytes + 1},
			wantStatus: http.StatusRequestEntityTooLarge,
			wantCode:   response.CodeUploadTooLarge,
		},
	}

	for _, tc := range rejectCases {
		t.Run("PrepareUpload/"+tc.name, func(t *testing.T) {
			rec, env := doJSON(http.MethodPost, "/api/v1/memes/upload", tc.payload)
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if env.Code != tc.wantCode {
				t.Errorf("code = %d, want %d (body: %s)", env.Code, tc.wantCode, rec.Body.String())
			}
		})
	}

	// Ensure rejected PrepareUpload requests did not create orphan DB rows.
	_, total, err := repo.ListByUser(ctx, userID, 50, 0)
	if err != nil {
		t.Fatalf("list memes: %v", err)
	}
	if total != 0 {
		t.Fatalf("expected 0 orphan DB rows after rejected uploads, got %d", total)
	}

	// 2. Valid PrepareUpload at exact 5 MiB boundary signs both content-length and content-type.
	rec, env := doJSON(http.MethodPost, "/api/v1/memes/upload", map[string]any{
		"title":       "boundary",
		"filename":    "ok.png",
		"contentType": "image/png",
		"fileSize":    testMaxUploadBytes,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("valid PrepareUpload status = %d, want %d (body: %s)", rec.Code, http.StatusCreated, rec.Body.String())
	}
	var prepared meme.UploadResponse
	if err := json.Unmarshal(env.Data, &prepared); err != nil {
		t.Fatalf("decode prepared data: %v", err)
	}
	assertSignedHeaders(t, prepared.UploadURL, "content-length", "content-type")

	// 3. ResultUpload error status mapping & valid presign.
	t.Run("ResultUpload/invalid json", func(t *testing.T) {
		rec, env := doJSON(http.MethodPost, "/api/v1/memes/"+prepared.ID+"/result-upload", "{bad-json")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
		if env.Code != response.CodeInvalidRequestBody {
			t.Errorf("code = %d, want %d", env.Code, response.CodeInvalidRequestBody)
		}
	})

	t.Run("ResultUpload/zero fileSize", func(t *testing.T) {
		rec, env := doJSON(http.MethodPost, "/api/v1/memes/"+prepared.ID+"/result-upload", map[string]any{"fileSize": 0})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
		if env.Code != response.CodeInvalidImage {
			t.Errorf("code = %d, want %d", env.Code, response.CodeInvalidImage)
		}
	})

	t.Run("ResultUpload/oversized fileSize", func(t *testing.T) {
		rec, env := doJSON(http.MethodPost, "/api/v1/memes/"+prepared.ID+"/result-upload", map[string]any{"fileSize": testMaxUploadBytes + 1})
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
		}
		if env.Code != response.CodeUploadTooLarge {
			t.Errorf("code = %d, want %d", env.Code, response.CodeUploadTooLarge)
		}
	})

	t.Run("ResultUpload/valid at boundary", func(t *testing.T) {
		rec, env := doJSON(http.MethodPost, "/api/v1/memes/"+prepared.ID+"/result-upload", map[string]any{"fileSize": testMaxUploadBytes})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
		}
		var res meme.UploadResponse
		if err := json.Unmarshal(env.Data, &res); err != nil {
			t.Fatalf("decode result data: %v", err)
		}
		assertSignedHeaders(t, res.UploadURL, "content-length", "content-type")
	})
}
