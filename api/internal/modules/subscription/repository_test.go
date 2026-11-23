package subscription_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
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

func TestCheckout_ReplaysSameIdempotencyKeyConcurrently(t *testing.T) {
	pool := newTestPool(t)
	userID := seedTestUser(t, pool)
	repo := subscription.NewPostgresRepository(pool)
	svc := subscription.NewService(repo, subscription.MockGateway{})

	const n = 10
	idempotencyKey := fmt.Sprintf("tkt105-same-%d", time.Now().UnixNano())
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]subscription.CheckoutResponse, n)
	errs := make([]error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			results[idx], errs[idx] = svc.Checkout(context.Background(), userID, subscription.CheckoutDTO{PlanCode: "premium_1d"}, idempotencyKey)
		}(i)
	}
	close(start)
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d failed: %v", i, errs[i])
		}
		if results[i].Subscription.ID != results[0].Subscription.ID || results[i].Payment.Reference != results[0].Payment.Reference {
			t.Fatalf("goroutine %d got (%s, %s), want (%s, %s)",
				i, results[i].Subscription.ID, results[i].Payment.Reference,
				results[0].Subscription.ID, results[0].Payment.Reference)
		}
	}

	var subCount, payCount int
	if err := pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM subscriptions WHERE user_id = $1`, userID).Scan(&subCount); err != nil {
		t.Fatalf("count subscriptions: %v", err)
	}
	if err := pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM payments p JOIN subscriptions s ON s.id = p.subscription_id WHERE s.user_id = $1`, userID).Scan(&payCount); err != nil {
		t.Fatalf("count payments: %v", err)
	}
	if subCount != 1 || payCount != 1 {
		t.Fatalf("expected 1 subscription and 1 payment, got %d subscriptions and %d payments", subCount, payCount)
	}
}

func TestCheckout_DeduplicatesPendingCheckoutAcrossNewIdempotencyKeys(t *testing.T) {
	pool := newTestPool(t)
	userID := seedTestUser(t, pool)
	repo := subscription.NewPostgresRepository(pool)
	svc := subscription.NewService(repo, subscription.MockGateway{})

	res1, err := svc.Checkout(context.Background(), userID, subscription.CheckoutDTO{PlanCode: "premium_1d"}, fmt.Sprintf("tkt105-k1-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("first checkout failed: %v", err)
	}

	res2, err := svc.Checkout(context.Background(), userID, subscription.CheckoutDTO{PlanCode: "premium_1d"}, fmt.Sprintf("tkt105-k2-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("second checkout failed: %v", err)
	}

	if res2.Subscription.ID != res1.Subscription.ID || res2.Payment.Reference != res1.Payment.Reference {
		t.Fatalf("expected replay of (%s, %s), got (%s, %s)",
			res1.Subscription.ID, res1.Payment.Reference, res2.Subscription.ID, res2.Payment.Reference)
	}

	var subCount int
	if err := pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM subscriptions WHERE user_id = $1 AND status = 'pending'`, userID).Scan(&subCount); err != nil {
		t.Fatalf("count pending subscriptions: %v", err)
	}
	if subCount != 1 {
		t.Fatalf("expected 1 pending subscription, got %d", subCount)
	}
}

func TestCheckout_AllowsDifferentPlanWhilePendingExists(t *testing.T) {
	pool := newTestPool(t)
	userID := seedTestUser(t, pool)
	repo := subscription.NewPostgresRepository(pool)
	svc := subscription.NewService(repo, subscription.MockGateway{})

	res1, err := svc.Checkout(context.Background(), userID, subscription.CheckoutDTO{PlanCode: "premium_1d"}, fmt.Sprintf("tkt105-p1-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("first plan checkout failed: %v", err)
	}

	res2, err := svc.Checkout(context.Background(), userID, subscription.CheckoutDTO{PlanCode: "premium_30d"}, fmt.Sprintf("tkt105-p2-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("second plan checkout failed: %v", err)
	}

	if res1.Subscription.ID == res2.Subscription.ID || res1.Payment.Reference == res2.Payment.Reference {
		t.Fatalf("expected distinct checkouts for different plans, got same (%s, %s)", res1.Subscription.ID, res1.Payment.Reference)
	}
}


func TestConfirmPayment_ActivatesSubscriptionAndRejectsDoubleConfirm(t *testing.T) {
	pool := newTestPool(t)
	userID := seedTestUser(t, pool)
	repo := subscription.NewPostgresRepository(pool)
	svc := subscription.NewService(repo, subscription.MockGateway{})

	checkout, err := svc.Checkout(context.Background(), userID, subscription.CheckoutDTO{PlanCode: "premium_1d"}, fmt.Sprintf("tkt106-confirm-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("checkout failed: %v", err)
	}

	if _, _, err := svc.Confirm(context.Background(), checkout.Payment.Reference); err != nil {
		t.Fatalf("first Confirm failed: %v", err)
	}

	var payStatus string
	if err := pool.QueryRow(context.Background(), `SELECT status FROM payments WHERE reference = $1`, checkout.Payment.Reference).Scan(&payStatus); err != nil {
		t.Fatalf("query payment status: %v", err)
	}
	if payStatus != "paid" {
		t.Fatalf("payment status = %q, want %q", payStatus, "paid")
	}

	active, _, err := repo.ActiveByUser(context.Background(), userID)
	if err != nil {
		t.Fatalf("ActiveByUser failed: %v", err)
	}
	if active.Status != "active" || active.StartsAt == nil || active.EndsAt == nil {
		t.Fatalf("expected active subscription with starts_at and ends_at, got %+v", active)
	}

	_, _, err = svc.Confirm(context.Background(), checkout.Payment.Reference)
	if !errors.Is(err, subscription.ErrPaymentFailed) {
		t.Fatalf("second Confirm err = %v, want ErrPaymentFailed", err)
	}
}

func TestConfirmPayment_RejectsUnknownReference(t *testing.T) {
	pool := newTestPool(t)
	repo := subscription.NewPostgresRepository(pool)
	svc := subscription.NewService(repo, subscription.MockGateway{})

	_, _, err := svc.Confirm(context.Background(), fmt.Sprintf("unknown-ref-%d", time.Now().UnixNano()))
	if !errors.Is(err, subscription.ErrPaymentFailed) {
		t.Fatalf("Confirm(unknown) err = %v, want ErrPaymentFailed", err)
	}
}

func TestConfirmPayment_ConcurrentConfirms_OnlyOneSucceeds(t *testing.T) {
	pool := newTestPool(t)
	userID := seedTestUser(t, pool)
	repo := subscription.NewPostgresRepository(pool)
	svc := subscription.NewService(repo, subscription.MockGateway{})

	checkout, err := svc.Checkout(context.Background(), userID, subscription.CheckoutDTO{PlanCode: "premium_1d"}, fmt.Sprintf("tkt106-race-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("checkout failed: %v", err)
	}

	const n = 10
	errs := make(chan error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, _, err := svc.Confirm(context.Background(), checkout.Payment.Reference)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)

	var succeeded, failed int
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, subscription.ErrPaymentFailed):
			failed++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if succeeded != 1 || failed != n-1 {
		t.Fatalf("expected 1 success and %d ErrPaymentFailed, got %d success and %d failed", n-1, succeeded, failed)
	}
}
