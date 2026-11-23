package subscription_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nyxsr/meme-portal/api/internal/middleware"
	"github.com/nyxsr/meme-portal/api/internal/modules/subscription"
	"github.com/nyxsr/meme-portal/api/internal/platform/cookie"
	"github.com/nyxsr/meme-portal/api/internal/platform/token"
)

func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	var candidates []string
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		candidates = append(candidates, dsn)
	} else {
		candidates = []string{
			"postgres://meme:meme@localhost:5433/meme_portal?sslmode=disable",
			"postgres://meme:meme@localhost:5432/meme_portal?sslmode=disable",
		}
	}

	var lastErr error
	for _, dsn := range candidates {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		pool, err := pgxpool.New(ctx, dsn)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}
		if err := pool.Ping(ctx); err != nil {
			cancel()
			pool.Close()
			lastErr = err
			continue
		}
		cancel()
		t.Cleanup(pool.Close)
		return pool
	}
	t.Skipf("postgres unavailable: %v", lastErr)
	return nil
}

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

func setupSubscriptionApp(pool *pgxpool.Pool, tokens *token.Manager) http.Handler {
	mux := http.NewServeMux()
	h := subscription.NewModule(pool)
	subscription.RegisterRoutes(mux, h, middleware.Auth(tokens))
	return middleware.CORS("http://localhost:3000")(mux)
}

func TestRegisterRoutes_StatusAuthenticated_PrivateNoStoreAndVary(t *testing.T) {
	pool := newTestPool(t)
	tokens := token.NewManager("test-secret-tkt102", "meme-portal", 15*time.Minute, time.Hour, nil)
	pair, err := tokens.Issue(context.Background(), "00000000-0000-0000-0000-000000000102", "user")
	if err != nil {
		t.Fatalf("Issue token failed: %v", err)
	}

	appHandler := setupSubscriptionApp(pool, tokens)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/subscription/status", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.AddCookie(&http.Cookie{Name: cookie.AccessTokenName, Value: pair.AccessToken})
	rec := httptest.NewRecorder()

	appHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}

	cc := rec.Header().Get("Cache-Control")
	if cc != "private, no-store" {
		t.Errorf("Cache-Control = %q, want %q", cc, "private, no-store")
	}
	if strings.Contains(strings.ToLower(cc), "public") || strings.Contains(strings.ToLower(cc), "max-age") {
		t.Errorf("Cache-Control %q must not contain public or max-age", cc)
	}

	vary := varyTokens(rec.Header())
	for _, want := range []string{"Origin", "Cookie", "Authorization"} {
		if !vary[strings.ToLower(want)] {
			t.Errorf("Vary %v missing %q", rec.Header().Values("Vary"), want)
		}
	}
}

func TestRegisterRoutes_StatusUnauthenticated_PrivateNoStoreAndVary(t *testing.T) {
	tokens := token.NewManager("test-secret-tkt102", "meme-portal", 15*time.Minute, time.Hour, nil)
	appHandler := setupSubscriptionApp(nil, tokens)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/subscription/status", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	rec := httptest.NewRecorder()

	appHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	cc := rec.Header().Get("Cache-Control")
	if cc != "private, no-store" {
		t.Errorf("Cache-Control = %q, want %q", cc, "private, no-store")
	}

	vary := varyTokens(rec.Header())
	for _, want := range []string{"Origin", "Cookie", "Authorization"} {
		if !vary[strings.ToLower(want)] {
			t.Errorf("Vary %v missing %q", rec.Header().Values("Vary"), want)
		}
	}
}

func TestRegisterRoutes_PlansPublic_NotProtectedAndNoBuggyPublicMaxAge(t *testing.T) {
	pool := newTestPool(t)
	tokens := token.NewManager("test-secret-tkt102", "meme-portal", 15*time.Minute, time.Hour, nil)
	appHandler := setupSubscriptionApp(pool, tokens)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/subscription/plans", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	rec := httptest.NewRecorder()

	appHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}

	cc := rec.Header().Get("Cache-Control")
	if strings.Contains(strings.ToLower(cc), "public") || strings.Contains(strings.ToLower(cc), "max-age=86400") {
		t.Errorf("Cache-Control = %q, should not emit public max-age=86400", cc)
	}
	if cc == "private, no-store" {
		t.Errorf("public plans endpoint should not be wrapped with auth private, no-store")
	}

	vary := varyTokens(rec.Header())
	if !vary["origin"] {
		t.Errorf("Vary %v missing Origin", rec.Header().Values("Vary"))
	}
}

func TestRegisterRoutes_ConfirmPayment_HTTPStatusCodes(t *testing.T) {
	pool := newTestPool(t)
	userID := seedTestUser(t, pool)
	repo := subscription.NewPostgresRepository(pool)
	svc := subscription.NewService(repo, subscription.MockGateway{})

	checkout, err := svc.Checkout(context.Background(), userID, subscription.CheckoutDTO{PlanCode: "premium_1d"}, fmt.Sprintf("tkt106-http-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("checkout failed: %v", err)
	}

	tokens := token.NewManager("test-secret-tkt106", "meme-portal", 15*time.Minute, time.Hour, nil)
	appHandler := setupSubscriptionApp(pool, tokens)

	confirmURL := "/api/v1/subscription/payments/" + checkout.Payment.Reference + "/confirm"

	// First confirm -> 200 OK
	req1 := httptest.NewRequest(http.MethodPost, confirmURL, nil)
	rec1 := httptest.NewRecorder()
	appHandler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first confirm status = %d, want %d (body: %s)", rec1.Code, http.StatusOK, rec1.Body.String())
	}

	// Duplicate confirm -> 402 Payment Required
	req2 := httptest.NewRequest(http.MethodPost, confirmURL, nil)
	rec2 := httptest.NewRecorder()
	appHandler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusPaymentRequired {
		t.Fatalf("duplicate confirm status = %d, want %d (body: %s)", rec2.Code, http.StatusPaymentRequired, rec2.Body.String())
	}

	// Unknown reference -> 402 Payment Required
	req3 := httptest.NewRequest(http.MethodPost, "/api/v1/subscription/payments/unknown-ref-tkt106/confirm", nil)
	rec3 := httptest.NewRecorder()
	appHandler.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusPaymentRequired {
		t.Fatalf("unknown ref confirm status = %d, want %d (body: %s)", rec3.Code, http.StatusPaymentRequired, rec3.Body.String())
	}
}
