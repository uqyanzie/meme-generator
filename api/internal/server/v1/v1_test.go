package v1

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nyxsr/meme-portal/api/internal/modules/meme"
	"github.com/nyxsr/meme-portal/api/internal/modules/subscription"
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

func TestSubscriptionGate_BlocksExpiredSubscription(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()

	var userID string
	email := fmt.Sprintf("tkt101-gate-%d@example.com", time.Now().UnixNano())
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ($1, 'Gate Test', 'hash') RETURNING id`,
		email,
	).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})

	var planID string
	if err := pool.QueryRow(ctx, `SELECT id FROM plans WHERE code = 'premium_1d' LIMIT 1`).Scan(&planID); err != nil {
		t.Fatalf("lookup plan: %v", err)
	}

	var subID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO subscriptions (user_id, plan_id, status, starts_at, ends_at)
		 VALUES ($1, $2, 'active', now() - interval '2 days', now() - interval '1 minute')
		 RETURNING id`,
		userID, planID,
	).Scan(&subID); err != nil {
		t.Fatalf("seed expired subscription: %v", err)
	}

	subSvc := subscription.NewService(subscription.NewPostgresRepository(pool), subscription.MockGateway{})
	gate := subscriptionGate{sub: subSvc}

	err := gate.CanCreate(ctx, userID)
	if !errors.Is(err, meme.ErrNotSubscribedGate) {
		t.Fatalf("expected ErrNotSubscribedGate for expired subscription, got: %v", err)
	}

	// Update ends_at to the future and verify CanCreate permits creation.
	if _, err := pool.Exec(ctx,
		`UPDATE subscriptions SET ends_at = now() + interval '1 hour' WHERE id = $1`,
		subID,
	); err != nil {
		t.Fatalf("update subscription ends_at: %v", err)
	}

	if err := gate.CanCreate(ctx, userID); err != nil {
		t.Fatalf("expected CanCreate to succeed for unexpired subscription, got: %v", err)
	}
}
