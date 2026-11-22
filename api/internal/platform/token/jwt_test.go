package token_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

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

func TestIssue_GeneratesUniqueJTI(t *testing.T) {
	mgr := token.NewManager("test-secret", "meme-portal-test", 15*time.Minute, time.Hour, nil)
	ctx := context.Background()

	p1, err := mgr.Issue(ctx, "user-jti-1", "user")
	if err != nil {
		t.Fatalf("Issue p1 failed: %v", err)
	}
	p2, err := mgr.Issue(ctx, "user-jti-1", "user")
	if err != nil {
		t.Fatalf("Issue p2 failed: %v", err)
	}

	c1, err := mgr.ParseAccess(p1.AccessToken)
	if err != nil {
		t.Fatalf("ParseAccess p1 failed: %v", err)
	}
	c2, err := mgr.ParseAccess(p2.AccessToken)
	if err != nil {
		t.Fatalf("ParseAccess p2 failed: %v", err)
	}
	if c1.ID == "" || c2.ID == "" {
		t.Fatalf("expected non-empty jti claims, got c1.ID=%q c2.ID=%q", c1.ID, c2.ID)
	}
	if c1.ID == c2.ID {
		t.Fatalf("expected unique jti per token, both were %q", c1.ID)
	}
}

func TestRevokeAccess_DenylistAndPastExpiryGuard(t *testing.T) {
	rdb := newTestRedis(t)
	mgr := token.NewManager("test-secret", "meme-portal-test", 15*time.Minute, time.Hour, rdb)
	ctx := context.Background()
	userID := "tkt103-test-" + token.RandomHex(8)

	t.Cleanup(func() {
		_ = rdb.Del(context.Background(), "auth:refresh:"+userID).Err()
	})

	p1, err := mgr.Issue(ctx, userID, "user")
	if err != nil {
		t.Fatalf("Issue p1 failed: %v", err)
	}
	p2, err := mgr.Issue(ctx, userID, "user")
	if err != nil {
		t.Fatalf("Issue p2 failed: %v", err)
	}

	c1, err := mgr.VerifyAccess(ctx, p1.AccessToken)
	if err != nil {
		t.Fatalf("VerifyAccess p1 before revoke failed: %v", err)
	}
	t.Cleanup(func() {
		_ = rdb.Del(context.Background(), "auth:revoked:access:"+c1.ID).Err()
	})

	// Revoke p1 access token.
	if err := mgr.RevokeAccess(ctx, c1.ID, c1.ExpiresAt.Time); err != nil {
		t.Fatalf("RevokeAccess failed: %v", err)
	}

	// p1 must now be rejected with ErrTokenRevoked.
	if _, err := mgr.VerifyAccess(ctx, p1.AccessToken); !errors.Is(err, token.ErrTokenRevoked) {
		t.Fatalf("expected ErrTokenRevoked for revoked access token, got %v", err)
	}

	// p2 (different jti for same user) must still be accepted.
	if _, err := mgr.VerifyAccess(ctx, p2.AccessToken); err != nil {
		t.Fatalf("expected p2 access token to remain valid, got %v", err)
	}

	// RevokeAccess with past expiresAt must NOT write an immortal key to Redis.
	pastJTI := "past-jti-" + token.RandomHex(6)
	t.Cleanup(func() {
		_ = rdb.Del(context.Background(), "auth:revoked:access:"+pastJTI).Err()
	})
	if err := mgr.RevokeAccess(ctx, pastJTI, time.Now().Add(-1*time.Minute)); err != nil {
		t.Fatalf("RevokeAccess with past expiry returned error: %v", err)
	}
	exists, err := rdb.Exists(ctx, "auth:revoked:access:"+pastJTI).Result()
	if err != nil {
		t.Fatalf("redis Exists failed: %v", err)
	}
	if exists != 0 {
		t.Fatalf("expected no Redis key for past-expiry RevokeAccess, but key exists")
	}

	// Revoke refresh token and verify VerifyRefresh rejects it.
	if err := mgr.Revoke(ctx, userID); err != nil {
		t.Fatalf("Revoke failed: %v", err)
	}
	if _, err := mgr.VerifyRefresh(ctx, p2.RefreshToken); !errors.Is(err, token.ErrTokenRevoked) {
		t.Fatalf("expected ErrTokenRevoked for revoked refresh token, got %v", err)
	}
}
