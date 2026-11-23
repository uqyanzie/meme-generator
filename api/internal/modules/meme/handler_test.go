package meme_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
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

func assertSignedUploadURL(t *testing.T, rawURL string) {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse upload url %q: %v", rawURL, err)
	}
	if parsed.Path != "/api/v1/memes/upload-object" {
		t.Errorf("upload url path = %q, want /api/v1/memes/upload-object", parsed.Path)
	}
	if parsed.Query().Get("token") == "" {
		t.Errorf("expected non-empty token query param in %q", rawURL)
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

	var (
		mu         sync.Mutex
		s3PutCount int
		lastPutCT  string
		lastPutLen int
	)
	s3Srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		if r.Method == http.MethodPut {
			s3PutCount++
			lastPutCT = r.Header.Get("Content-Type")
			lastPutLen = len(body)
		}
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer s3Srv.Close()

	repo := meme.NewPostgresRepository(pool)
	svc := meme.NewService(repo, newStubS3Client(t, s3Srv.URL), config.StorageConfig{
		Bucket:        "meme-portal",
		PublicBaseURL: "http://localhost:3902/meme-portal",
	}, stubGate{}, "test-secret")
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
			name:       "explicit zero fileSize",
			payload:    map[string]any{"title": "zero", "filename": "ok.png", "contentType": "image/png", "fileSize": 0},
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
		{
			name:       "oversized size alias (5 MiB + 1)",
			payload:    map[string]any{"title": "huge-alias", "filename": "huge.png", "contentType": "image/png", "size": testMaxUploadBytes + 1},
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

	// 2. Valid PrepareUpload at exact 5 MiB boundary returns signed upload URL.
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
	assertSignedUploadURL(t, prepared.UploadURL)

	// 2b. Backward-compatible PrepareUpload (omitting fileSize) STILL enforces <= 5 MiB on PUT!
	var compatUploadURL string
	t.Run("PrepareUpload/omit fileSize", func(t *testing.T) {
		rec, env := doJSON(http.MethodPost, "/api/v1/memes/upload", map[string]any{
			"title":       "compat-api",
			"filename":    "ok.png",
			"contentType": "image/png",
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusCreated, rec.Body.String())
		}
		var res meme.UploadResponse
		if err := json.Unmarshal(env.Data, &res); err != nil {
			t.Fatalf("decode data: %v", err)
		}
		assertSignedUploadURL(t, res.UploadURL)
		compatUploadURL = res.UploadURL
	})

	t.Run("PrepareUpload/omit contentType and fileSize", func(t *testing.T) {
		rec, env := doJSON(http.MethodPost, "/api/v1/memes/upload", map[string]any{
			"title":    "compat-web",
			"filename": "ok.jpg",
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusCreated, rec.Body.String())
		}
		var res meme.UploadResponse
		if err := json.Unmarshal(env.Data, &res); err != nil {
			t.Fatalf("decode data: %v", err)
		}
		assertSignedUploadURL(t, res.UploadURL)
	})

	// 2c. UploadObject enforcement tests (TKT-108 regression: 11MB upload when fileSize was omitted).
	doPut := func(rawURL string, body []byte, contentType string, unknownLength bool) *httptest.ResponseRecorder {
		t.Helper()
		parsed, err := url.Parse(rawURL)
		if err != nil {
			t.Fatalf("parse url: %v", err)
		}
		var reader io.Reader = bytes.NewReader(body)
		if unknownLength {
			reader = struct{ io.Reader }{bytes.NewReader(body)}
		}
		req := httptest.NewRequest(http.MethodPut, parsed.RequestURI(), reader)
		if unknownLength {
			req.ContentLength = -1
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	t.Run("UploadObject/reject 11MB when fileSize omitted", func(t *testing.T) {
		elevenMB := make([]byte, 11<<20)
		rec := doPut(compatUploadURL, elevenMB, "image/png", false)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("11MB PUT status = %d, want %d (body: %s)", rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
		}
		mu.Lock()
		puts := s3PutCount
		mu.Unlock()
		if puts != 0 {
			t.Fatalf("expected 0 S3 puts for rejected 11MB upload, got %d", puts)
		}
	})

	t.Run("UploadObject/reject chunked >5MiB when Content-Length omitted", func(t *testing.T) {
		overLimit := make([]byte, testMaxUploadBytes+1024)
		rec := doPut(compatUploadURL, overLimit, "image/png", true)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("chunked >5MiB PUT status = %d, want %d (body: %s)", rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
		}
	})

	t.Run("UploadObject/reject tampered Content-Type", func(t *testing.T) {
		rec := doPut(compatUploadURL, []byte("<html>xss</html>"), "text/html", false)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("tampered CT status = %d, want %d (body: %s)", rec.Code, http.StatusForbidden, rec.Body.String())
		}
	})

	t.Run("UploadObject/reject tampered size when fileSize declared", func(t *testing.T) {
		rec := doPut(prepared.UploadURL, []byte("small-payload"), "image/png", false)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("tampered size status = %d, want %d (body: %s)", rec.Code, http.StatusForbidden, rec.Body.String())
		}
	})

	t.Run("UploadObject/reject invalid token", func(t *testing.T) {
		rec := doPut("http://example.com/api/v1/memes/upload-object?token=forged.token", []byte("png"), "image/png", false)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("invalid token status = %d, want %d", rec.Code, http.StatusForbidden)
		}
	})

	t.Run("UploadObject/reject empty body", func(t *testing.T) {
		rec := doPut(compatUploadURL, nil, "image/png", false)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("empty body status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("UploadObject/accept valid small upload and forward to S3", func(t *testing.T) {
		payload := []byte("valid-png-bytes")
		rec := doPut(compatUploadURL, payload, "image/png", false)
		if rec.Code != http.StatusOK {
			t.Fatalf("valid PUT status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
		}
		mu.Lock()
		puts, ct, gotLen := s3PutCount, lastPutCT, lastPutLen
		mu.Unlock()
		if puts != 1 || ct != "image/png" || gotLen != len(payload) {
			t.Fatalf("unexpected S3 put: count=%d ct=%q len=%d", puts, ct, gotLen)
		}
	})

	// 3. ResultUpload error status mapping, backward-compatible empty body & valid signed URL.
	t.Run("ResultUpload/empty body backward-compatible", func(t *testing.T) {
		rec, env := doJSON(http.MethodPost, "/api/v1/memes/"+prepared.ID+"/result-upload", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
		}
		var res meme.UploadResponse
		if err := json.Unmarshal(env.Data, &res); err != nil {
			t.Fatalf("decode result data: %v", err)
		}
		assertSignedUploadURL(t, res.UploadURL)

		// 11MB PUT to result upload URL is also rejected.
		rec11 := doPut(res.UploadURL, make([]byte, 11<<20), "image/png", false)
		if rec11.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("result 11MB PUT status = %d, want %d", rec11.Code, http.StatusRequestEntityTooLarge)
		}
	})

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
		assertSignedUploadURL(t, res.UploadURL)
	})
}
