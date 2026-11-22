package auth_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/nyxsr/meme-portal/api/internal/middleware"
	"github.com/nyxsr/meme-portal/api/internal/modules/auth"
	"github.com/nyxsr/meme-portal/api/internal/platform/cookie"
	"github.com/nyxsr/meme-portal/api/internal/platform/token"
)

func newTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: os.Getenv("REDIS_PASSWORD"),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		t.Skipf("redis unavailable at %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

func assertAuthCookiesCleared(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	cleared := map[string]*http.Cookie{}
	for _, c := range rec.Result().Cookies() {
		cleared[c.Name] = c
	}
	for _, name := range []string{cookie.AccessTokenName, cookie.RefreshTokenName} {
		c, ok := cleared[name]
		if !ok {
			t.Fatalf("expected Set-Cookie to clear %q, but it was missing", name)
		}
		if c.Path != "/" {
			t.Errorf("cookie %q: expected Path=/, got %q", name, c.Path)
		}
		if c.MaxAge != -1 {
			t.Errorf("cookie %q: expected MaxAge=-1, got %d", name, c.MaxAge)
		}
		if !c.HttpOnly {
			t.Errorf("cookie %q: expected HttpOnly=true, got false", name)
		}
		if c.Value != "" {
			t.Errorf("cookie %q: expected empty value, got %q", name, c.Value)
		}
	}
}

func setupAuthMux(tokens *token.Manager) *http.ServeMux {
	mux := http.NewServeMux()
	svc := auth.NewService(nil, tokens)
	h := auth.NewHandler(svc, tokens)
	authMW := middleware.Auth(tokens)
	auth.RegisterRoutes(mux, h, authMW)
	// Lightweight protected endpoint to test middleware.Auth without DB.
	mux.Handle("GET /api/v1/test-protected", authMW(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	return mux
}

func TestLogout_ActiveSession_RevokesTokensAndClearsCookies(t *testing.T) {
	rdb := newTestRedis(t)
	tokens := token.NewManager("test-secret", "meme-portal-test", 15*time.Minute, time.Hour, rdb)
	mux := setupAuthMux(tokens)

	ctx := context.Background()
	userID := "tkt103-logout-" + token.RandomHex(8)
	pair, err := tokens.Issue(ctx, userID, "user")
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	claims, err := tokens.ParseAccess(pair.AccessToken)
	if err != nil {
		t.Fatalf("ParseAccess failed: %v", err)
	}
	t.Cleanup(func() {
		_ = rdb.Del(context.Background(), "auth:refresh:"+userID, "auth:revoked:access:"+claims.ID).Err()
	})

	// 1. Protected route succeeds before logout.
	reqBefore := httptest.NewRequest(http.MethodGet, "/api/v1/test-protected", nil)
	reqBefore.AddCookie(&http.Cookie{Name: cookie.AccessTokenName, Value: pair.AccessToken})
	recBefore := httptest.NewRecorder()
	mux.ServeHTTP(recBefore, reqBefore)
	if recBefore.Code != http.StatusOK {
		t.Fatalf("expected 200 before logout, got %d", recBefore.Code)
	}

	// 2. Logout returns 200 and clears both cookies on Path=/.
	reqLogout := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	reqLogout.AddCookie(&http.Cookie{Name: cookie.AccessTokenName, Value: pair.AccessToken})
	reqLogout.AddCookie(&http.Cookie{Name: cookie.RefreshTokenName, Value: pair.RefreshToken})
	recLogout := httptest.NewRecorder()
	mux.ServeHTTP(recLogout, reqLogout)
	if recLogout.Code != http.StatusOK {
		t.Fatalf("expected 200 on logout, got %d", recLogout.Code)
	}
	assertAuthCookiesCleared(t, recLogout)

	// 3. Replaying access token after logout is rejected with 401.
	reqAfter := httptest.NewRequest(http.MethodGet, "/api/v1/test-protected", nil)
	reqAfter.AddCookie(&http.Cookie{Name: cookie.AccessTokenName, Value: pair.AccessToken})
	recAfter := httptest.NewRecorder()
	mux.ServeHTTP(recAfter, reqAfter)
	if recAfter.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 after logout, got %d", recAfter.Code)
	}

	// 4. Refresh token is revoked in Redis.
	if _, err := tokens.VerifyRefresh(ctx, pair.RefreshToken); !errors.Is(err, token.ErrTokenRevoked) {
		t.Fatalf("expected ErrTokenRevoked for refresh token, got %v", err)
	}

	// 5. Repeated logout (idempotency) still returns 200 and clears cookies.
	reqRepeat := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	reqRepeat.AddCookie(&http.Cookie{Name: cookie.AccessTokenName, Value: pair.AccessToken})
	reqRepeat.AddCookie(&http.Cookie{Name: cookie.RefreshTokenName, Value: pair.RefreshToken})
	recRepeat := httptest.NewRecorder()
	mux.ServeHTTP(recRepeat, reqRepeat)
	if recRepeat.Code != http.StatusOK {
		t.Fatalf("expected 200 on repeated logout, got %d", recRepeat.Code)
	}
	assertAuthCookiesCleared(t, recRepeat)
}

func TestLogout_ExpiredAccessWithValidRefresh_RevokesRefreshAndClearsCookies(t *testing.T) {
	rdb := newTestRedis(t)
	// Issue with already-expired access token (-1s) and valid refresh token (1h).
	tokens := token.NewManager("test-secret", "meme-portal-test", -1*time.Second, time.Hour, rdb)
	mux := setupAuthMux(tokens)

	ctx := context.Background()
	userID := "tkt103-expired-" + token.RandomHex(8)
	pair, err := tokens.Issue(ctx, userID, "user")
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	t.Cleanup(func() {
		_ = rdb.Del(context.Background(), "auth:refresh:"+userID).Err()
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: cookie.AccessTokenName, Value: pair.AccessToken})
	req.AddCookie(&http.Cookie{Name: cookie.RefreshTokenName, Value: pair.RefreshToken})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on logout with expired access token, got %d", rec.Code)
	}
	assertAuthCookiesCleared(t, rec)

	if _, err := tokens.VerifyRefresh(ctx, pair.RefreshToken); !errors.Is(err, token.ErrTokenRevoked) {
		t.Fatalf("expected refresh token to be revoked, got %v", err)
	}
}

func TestLogout_MissingOrInvalidTokens_StillReturns200AndClearsCookies(t *testing.T) {
	tokens := token.NewManager("test-secret", "meme-portal-test", 15*time.Minute, time.Hour, nil)
	mux := setupAuthMux(tokens)

	cases := []struct {
		name    string
		cookies []*http.Cookie
	}{
		{name: "no cookies", cookies: nil},
		{
			name: "garbage cookies",
			cookies: []*http.Cookie{
				{Name: cookie.AccessTokenName, Value: "not-a-jwt"},
				{Name: cookie.RefreshTokenName, Value: "also-not-a-jwt"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
			for _, c := range tc.cookies {
				req.AddCookie(c)
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", rec.Code)
			}
			assertAuthCookiesCleared(t, rec)
		})
	}
}
