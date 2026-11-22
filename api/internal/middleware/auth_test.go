package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nyxsr/meme-portal/api/internal/middleware"
	"github.com/nyxsr/meme-portal/api/internal/platform/cookie"
	"github.com/nyxsr/meme-portal/api/internal/platform/token"
)

func varyTokens(h http.Header) map[string]bool {
	out := map[string]bool{}
	for _, raw := range h.Values("Vary") {
		for _, part := range strings.Split(raw, ",") {
			if v := strings.ToLower(strings.TrimSpace(part)); v != "" {
				out[v] = true
			}
		}
	}
	return out
}

func assertNoStoreAndVary(t *testing.T, rec *httptest.ResponseRecorder, expectedVary ...string) {
	t.Helper()
	gotCC := rec.Header().Get("Cache-Control")
	if gotCC != "private, no-store" {
		t.Errorf("Cache-Control = %q, want %q", gotCC, "private, no-store")
	}
	vary := varyTokens(rec.Header())
	for _, want := range expectedVary {
		if !vary[strings.ToLower(want)] {
			t.Errorf("Vary header %v missing %q", rec.Header().Values("Vary"), want)
		}
	}
}

func TestAuth_AuthenticatedRequests_SetPrivateNoStoreAndVary(t *testing.T) {
	tokens := token.NewManager("test-secret-tkt102", "meme-portal", 15*time.Minute, time.Hour, nil)
	pair, err := tokens.Issue(context.Background(), "user-102", "user")
	if err != nil {
		t.Fatalf("Issue token failed: %v", err)
	}

	handler := middleware.Auth(tokens)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, ok := middleware.UserIDFromContext(r.Context())
		if !ok || uid != "user-102" {
			t.Errorf("UserIDFromContext = (%q, %v), want (%q, true)", uid, ok, "user-102")
		}
		w.WriteHeader(http.StatusOK)
	}))

	cases := []struct {
		name  string
		setup func(req *http.Request)
	}{
		{
			name: "cookie auth",
			setup: func(req *http.Request) {
				req.AddCookie(&http.Cookie{Name: cookie.AccessTokenName, Value: pair.AccessToken})
			},
		},
		{
			name: "bearer header auth",
			setup: func(req *http.Request) {
				req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/protected", nil)
			tc.setup(req)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			assertNoStoreAndVary(t, rec, "Cookie", "Authorization")
		})
	}
}

func TestAuth_UnauthenticatedOrInvalid_SetPrivateNoStoreAndVary(t *testing.T) {
	tokens := token.NewManager("test-secret-tkt102", "meme-portal", 15*time.Minute, time.Hour, nil)

	handler := middleware.Auth(tokens)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called for unauthorized request")
		w.WriteHeader(http.StatusOK)
	}))

	cases := []struct {
		name  string
		setup func(req *http.Request)
	}{
		{
			name:  "missing credentials",
			setup: func(req *http.Request) {},
		},
		{
			name: "invalid bearer token",
			setup: func(req *http.Request) {
				req.Header.Set("Authorization", "Bearer not-a-valid-jwt")
			},
		},
		{
			name: "invalid cookie token",
			setup: func(req *http.Request) {
				req.AddCookie(&http.Cookie{Name: cookie.AccessTokenName, Value: "not-a-valid-jwt"})
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/protected", nil)
			tc.setup(req)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
			}
			assertNoStoreAndVary(t, rec, "Cookie", "Authorization")
		})
	}
}

func TestAuth_ComposedWithCORS_PreservesVaryOrigin(t *testing.T) {
	tokens := token.NewManager("test-secret-tkt102", "meme-portal", 15*time.Minute, time.Hour, nil)
	pair, err := tokens.Issue(context.Background(), "user-102", "user")
	if err != nil {
		t.Fatalf("Issue token failed: %v", err)
	}

	// Matches app.New composition: CORS wraps ServeMux which wraps middleware.Auth.
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/protected", middleware.Auth(tokens)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	appHandler := middleware.CORS("http://localhost:3000")(mux)

	t.Run("authenticated with Origin", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/protected", nil)
		req.Header.Set("Origin", "http://localhost:3000")
		req.AddCookie(&http.Cookie{Name: cookie.AccessTokenName, Value: pair.AccessToken})
		rec := httptest.NewRecorder()

		appHandler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		assertNoStoreAndVary(t, rec, "Origin", "Cookie", "Authorization")
	})

	t.Run("unauthenticated with Origin", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/protected", nil)
		req.Header.Set("Origin", "http://localhost:3000")
		rec := httptest.NewRecorder()

		appHandler.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
		assertNoStoreAndVary(t, rec, "Origin", "Cookie", "Authorization")
	})
}
