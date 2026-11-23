package v1

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nyxsr/meme-portal/api/internal/config"
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

	err := gate.HasActiveSubscription(ctx, userID)
	if !errors.Is(err, meme.ErrNotSubscribedGate) {
		t.Fatalf("expected ErrNotSubscribedGate for expired subscription, got: %v", err)
	}

	// Update ends_at to the future and verify HasActiveSubscription permits creation.
	if _, err := pool.Exec(ctx,
		`UPDATE subscriptions SET ends_at = now() + interval '1 hour' WHERE id = $1`,
		subID,
	); err != nil {
		t.Fatalf("update subscription ends_at: %v", err)
	}

	if err := gate.HasActiveSubscription(ctx, userID); err != nil {
		t.Fatalf("expected HasActiveSubscription to succeed for unexpired subscription, got: %v", err)
	}
}

func newTestMemeService(pool *pgxpool.Pool, gate meme.Gate) *meme.Service {
	s3Client := s3.New(s3.Options{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
	})
	cfg := config.StorageConfig{
		Bucket:        "memes",
		PublicBaseURL: "http://localhost:3902",
	}
	return meme.NewService(meme.NewPostgresRepository(pool), s3Client, cfg, gate)
}

func seedUserWithPlan(t *testing.T, pool *pgxpool.Pool, planCode string) (userID, subID string) {
	t.Helper()
	ctx := context.Background()

	email := fmt.Sprintf("tkt109-%s-%d@example.com", planCode, time.Now().UnixNano())
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ($1, 'TKT-109 Test', 'hash') RETURNING id`,
		email,
	).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})

	var planID string
	if err := pool.QueryRow(ctx, `SELECT id FROM plans WHERE code = $1 LIMIT 1`, planCode).Scan(&planID); err != nil {
		t.Fatalf("lookup plan %s: %v", planCode, err)
	}

	if err := pool.QueryRow(ctx,
		`INSERT INTO subscriptions (user_id, plan_id, status, starts_at, ends_at)
		 VALUES ($1, $2, 'active', now() - interval '1 hour', now() + interval '1 day')
		 RETURNING id`,
		userID, planID,
	).Scan(&subID); err != nil {
		t.Fatalf("seed subscription: %v", err)
	}
	return userID, subID
}

func TestMemeQuota_ConcurrentPrepareUploadEnforcesLimit(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	userID, _ := seedUserWithPlan(t, pool, "premium_1d") // max_memes = 10

	subSvc := subscription.NewService(subscription.NewPostgresRepository(pool), subscription.MockGateway{})
	gate := subscriptionGate{sub: subSvc}
	memeSvc := newTestMemeService(pool, gate)

	const totalRequests = 20
	size1024 := int64(1024)
	start := make(chan struct{})
	var wg sync.WaitGroup
	var succeeded atomic.Int32
	var blocked atomic.Int32
	var otherErrs atomic.Int32

	for i := 0; i < totalRequests; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			_, err := memeSvc.PrepareUpload(ctx, userID, fmt.Sprintf("Meme %d", idx), ".png", "image/png", &size1024)
			switch {
			case err == nil:
				succeeded.Add(1)
			case errors.Is(err, meme.ErrNotSubscribedGate):
				blocked.Add(1)
			default:
				otherErrs.Add(1)
				t.Logf("unexpected error on req %d: %v", idx, err)
			}
		}(i)
	}

	close(start)
	wg.Wait()

	if otherErrs.Load() != 0 {
		t.Fatalf("expected 0 unexpected errors, got %d", otherErrs.Load())
	}
	if succeeded.Load() != 10 {
		t.Fatalf("expected exactly 10 succeeded PrepareUpload calls, got %d (blocked=%d)", succeeded.Load(), blocked.Load())
	}
	if blocked.Load() != 10 {
		t.Fatalf("expected exactly 10 blocked PrepareUpload calls, got %d", blocked.Load())
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM memes WHERE user_id = $1`, userID).Scan(&count); err != nil {
		t.Fatalf("count memes: %v", err)
	}
	if count != 10 {
		t.Fatalf("expected 10 rows in memes table, got %d", count)
	}
}

func TestMemeQuota_FinalAllowedMemeCanResultUploadAndComplete(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	userID, subID := seedUserWithPlan(t, pool, "premium_1d") // max_memes = 10

	subSvc := subscription.NewService(subscription.NewPostgresRepository(pool), subscription.MockGateway{})
	gate := subscriptionGate{sub: subSvc}
	memeSvc := newTestMemeService(pool, gate)

	size1024 := int64(1024)
	var tenthMemeID string
	for i := 1; i <= 10; i++ {
		res, err := memeSvc.PrepareUpload(ctx, userID, fmt.Sprintf("Seq %d", i), ".png", "image/png", &size1024)
		if err != nil {
			t.Fatalf("PrepareUpload #%d failed: %v", i, err)
		}
		tenthMemeID = res.ID
	}

	// 11th meme must be blocked.
	if _, err := memeSvc.PrepareUpload(ctx, userID, "Seq 11", ".png", "image/png", &size1024); !errors.Is(err, meme.ErrNotSubscribedGate) {
		t.Fatalf("expected 11th PrepareUpload to fail with ErrNotSubscribedGate, got %v", err)
	}

	// 10th meme must still be allowed to ResultUpload and Complete.
	if _, err := memeSvc.ResultUpload(ctx, userID, tenthMemeID, &size1024); err != nil {
		t.Fatalf("expected ResultUpload on 10th meme to succeed, got: %v", err)
	}

	if _, err := memeSvc.Complete(ctx, userID, tenthMemeID, "http://localhost:3902/results/10.png"); err != nil {
		t.Fatalf("expected Complete on 10th meme to succeed, got: %v", err)
	}

	// Expire subscription and verify ResultUpload & Complete are now blocked.
	if _, err := pool.Exec(ctx, `UPDATE subscriptions SET ends_at = now() - interval '1 minute' WHERE id = $1`, subID); err != nil {
		t.Fatalf("expire subscription: %v", err)
	}
	if _, err := memeSvc.ResultUpload(ctx, userID, tenthMemeID, &size1024); !errors.Is(err, meme.ErrNotSubscribedGate) {
		t.Fatalf("expected ResultUpload after expiry to fail with ErrNotSubscribedGate, got %v", err)
	}
	if _, err := memeSvc.Complete(ctx, userID, tenthMemeID, "http://localhost:3902/results/expired.png"); !errors.Is(err, meme.ErrNotSubscribedGate) {
		t.Fatalf("expected Complete after expiry to fail with ErrNotSubscribedGate, got %v", err)
	}
}

func TestMemeQuota_UnlimitedPlanAllowsBeyondStandardLimits(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	userID, _ := seedUserWithPlan(t, pool, "premium_90d") // max_memes = 0 (unlimited)

	subSvc := subscription.NewService(subscription.NewPostgresRepository(pool), subscription.MockGateway{})
	gate := subscriptionGate{sub: subSvc}
	memeSvc := newTestMemeService(pool, gate)

	const totalRequests = 15
	size1024 := int64(1024)
	start := make(chan struct{})
	var wg sync.WaitGroup
	var succeeded atomic.Int32

	for i := 0; i < totalRequests; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			if _, err := memeSvc.PrepareUpload(ctx, userID, fmt.Sprintf("Unlim %d", idx), ".png", "image/png", &size1024); err == nil {
				succeeded.Add(1)
			} else {
				t.Logf("unexpected error on unlimited req %d: %v", idx, err)
			}
		}(i)
	}

	close(start)
	wg.Wait()

	if succeeded.Load() != totalRequests {
		t.Fatalf("expected all %d concurrent PrepareUpload calls to succeed on unlimited plan, got %d", totalRequests, succeeded.Load())
	}
}
