package subscription_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nyxsr/meme-portal/api/internal/modules/subscription"
)

func seedTestUser(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()
	email := fmt.Sprintf("tkt101-%d@example.com", time.Now().UnixNano())
	var userID string
	err := pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ($1, 'TKT-101 Test', 'hash') RETURNING id`,
		email,
	).Scan(&userID)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})
	return userID
}

func seedSubscription(t *testing.T, pool *pgxpool.Pool, userID, status string, endsAt *time.Time) string {
	t.Helper()
	ctx := context.Background()
	var planID string
	if err := pool.QueryRow(ctx, `SELECT id FROM plans WHERE code = 'premium_1d' LIMIT 1`).Scan(&planID); err != nil {
		t.Fatalf("lookup plan: %v", err)
	}
	var subID string
	err := pool.QueryRow(ctx,
		`INSERT INTO subscriptions (user_id, plan_id, status, starts_at, ends_at)
		 VALUES ($1, $2, $3, now() - interval '2 days', $4)
		 RETURNING id`,
		userID, planID, status, endsAt,
	).Scan(&subID)
	if err != nil {
		t.Fatalf("seed subscription: %v", err)
	}
	return subID
}

func TestActiveByUser_RejectsExpiredSubscription(t *testing.T) {
	pool := newTestPool(t)
	userID := seedTestUser(t, pool)
	expiredAt := time.Now().Add(-1 * time.Minute)
	seedSubscription(t, pool, userID, "active", &expiredAt)

	repo := subscription.NewPostgresRepository(pool)
	svc := subscription.NewService(repo, subscription.MockGateway{})

	_, _, err := repo.ActiveByUser(context.Background(), userID)
	if !errors.Is(err, subscription.ErrNotSubscribed) {
		t.Fatalf("expected ErrNotSubscribed for expired subscription, got: %v", err)
	}

	status, err := svc.Status(context.Background(), userID)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Subscribed {
		t.Fatalf("expected Subscribed=false for expired subscription, got true (endsAt=%v)", status.EndsAt)
	}
}

func TestActiveByUser_RejectsNullEndsAt(t *testing.T) {
	pool := newTestPool(t)
	userID := seedTestUser(t, pool)
	seedSubscription(t, pool, userID, "active", nil)

	repo := subscription.NewPostgresRepository(pool)
	_, _, err := repo.ActiveByUser(context.Background(), userID)
	if !errors.Is(err, subscription.ErrNotSubscribed) {
		t.Fatalf("expected ErrNotSubscribed for NULL ends_at, got: %v", err)
	}
}

func TestActiveByUser_ReturnsActiveUnexpiredSubscription(t *testing.T) {
	pool := newTestPool(t)
	userID := seedTestUser(t, pool)
	futureAt := time.Now().Add(1 * time.Hour)
	seedSubscription(t, pool, userID, "active", &futureAt)

	repo := subscription.NewPostgresRepository(pool)
	svc := subscription.NewService(repo, subscription.MockGateway{})

	sub, _, err := repo.ActiveByUser(context.Background(), userID)
	if err != nil {
		t.Fatalf("expected ActiveByUser to succeed, got: %v", err)
	}
	if sub.UserID != userID {
		t.Fatalf("unexpected userID: %s", sub.UserID)
	}

	status, err := svc.Status(context.Background(), userID)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status.Subscribed {
		t.Fatalf("expected Subscribed=true for unexpired subscription")
	}
}

func TestCheckout_AllowedAfterExpirationBeforeJanitor(t *testing.T) {
	pool := newTestPool(t)
	userID := seedTestUser(t, pool)
	expiredAt := time.Now().Add(-1 * time.Hour)
	seedSubscription(t, pool, userID, "active", &expiredAt)

	repo := subscription.NewPostgresRepository(pool)
	svc := subscription.NewService(repo, subscription.MockGateway{})

	res, err := svc.Checkout(context.Background(), userID, subscription.CheckoutDTO{PlanCode: "premium_1d"}, "127.0.0.1")
	if err != nil {
		t.Fatalf("expected Checkout to succeed after expiration, got: %v", err)
	}
	if res.Subscription.ID == "" {
		t.Fatalf("expected non-empty subscription ID")
	}
}
