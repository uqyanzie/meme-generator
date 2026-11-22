package subscription

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository akses data langganan & pembayaran.
type Repository struct {
	pool *pgxpool.Pool
}

func NewPostgresRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) ListPlans(ctx context.Context) ([]Plan, error) {
	const q = `
		SELECT id, code, name, price_idr, duration_days, max_memes, active
		FROM plans WHERE active = true ORDER BY price_idr ASC`
	rows, err := r.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("subscription repository: list plans gagal: %w", err)
	}
	defer rows.Close()
	var plans []Plan
	for rows.Next() {
		var p Plan
		if err := rows.Scan(&p.ID, &p.Code, &p.Name, &p.PriceIDR, &p.Duration, &p.MaxMemes, &p.Active); err != nil {
			return nil, err
		}
		plans = append(plans, p)
	}
	return plans, rows.Err()
}

func (r *Repository) GetPlanByCode(ctx context.Context, code string) (Plan, error) {
	const q = `
		SELECT id, code, name, price_idr, duration_days, max_memes, active
		FROM plans WHERE code = $1 AND active = true`
	var p Plan
	err := r.pool.QueryRow(ctx, q, code).Scan(&p.ID, &p.Code, &p.Name, &p.PriceIDR, &p.Duration, &p.MaxMemes, &p.Active)
	if errors.Is(err, pgx.ErrNoRows) {
		return Plan{}, ErrPlanNotFound
	}
	if err != nil {
		return Plan{}, fmt.Errorf("subscription repository: get plan gagal: %w", err)
	}
	return p, nil
}

// CreateSubscription membuat langganan pending + pembayaran pending dalam satu transaksi.
// idempotencyKey disimpan di payments: replay dengan key sama mengembalikan
// payment yang sama (unique index sebagai safety net).
func (r *Repository) CreateSubscription(ctx context.Context, userID string, p Plan, reference, idempotencyKey string) (Subscription, Payment, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Subscription{}, Payment{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var sub Subscription
	err = tx.QueryRow(ctx, `
		INSERT INTO subscriptions (user_id, plan_id, status)
		VALUES ($1, $2, 'pending')
		RETURNING id, user_id, plan_id, status, starts_at, ends_at`,
		userID, p.ID,
	).Scan(&sub.ID, &sub.UserID, &sub.PlanID, &sub.Status, &sub.StartsAt, &sub.EndsAt)
	if err != nil {
		return Subscription{}, Payment{}, fmt.Errorf("subscription repository: create subscription gagal: %w", err)
	}

	var pay Payment
	err = tx.QueryRow(ctx, `
		INSERT INTO payments (subscription_id, amount_idr, status, reference, idempotency_key)
		VALUES ($1, $2, 'pending', $3, $4)
		RETURNING id, subscription_id, amount_idr, status, reference`,
		sub.ID, p.PriceIDR, reference, idempotencyKey,
	).Scan(&pay.ID, &pay.SubscriptionID, &pay.AmountIDR, &pay.Status, &pay.Reference)
	if err != nil {
		return Subscription{}, Payment{}, fmt.Errorf("subscription repository: create payment gagal: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Subscription{}, Payment{}, err
	}
	return sub, pay, nil
}

// CheckoutByIdempotencyKey mengembalikan checkout yang sudah ada untuk key
// yang sama (replay). ok=false berarti belum ada.
func (r *Repository) CheckoutByIdempotencyKey(ctx context.Context, key string) (CheckoutResponse, bool, error) {
	const q = `
		SELECT s.id, s.user_id, s.plan_id, s.status, s.starts_at, s.ends_at,
		       p.id, p.subscription_id, p.amount_idr, p.status, p.reference
		FROM payments p
		JOIN subscriptions s ON s.id = p.subscription_id
		WHERE p.idempotency_key = $1`
	var (
		res CheckoutResponse
	)
	err := r.pool.QueryRow(ctx, q, key).Scan(
		&res.Subscription.ID, &res.Subscription.UserID, &res.Subscription.PlanID,
		&res.Subscription.Status, &res.Subscription.StartsAt, &res.Subscription.EndsAt,
		&res.Payment.ID, &res.Payment.SubscriptionID, &res.Payment.AmountIDR,
		&res.Payment.Status, &res.Payment.Reference,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return CheckoutResponse{}, false, nil
	}
	if err != nil {
		return CheckoutResponse{}, false, fmt.Errorf("subscription repository: lookup idempotency gagal: %w", err)
	}
	res.MockGatewayURL = "/api/v1/subscription/payments/" + res.Payment.Reference + "/confirm"
	return res, true, nil
}

// ConfirmPayment menandai pembayaran lunas (lookup via reference) dan
// mengaktifkan langganan (transaksi).
func (r *Repository) ConfirmPayment(ctx context.Context, reference string) (Subscription, Payment, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Subscription{}, Payment{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var pay Payment
	err = tx.QueryRow(ctx, `
		UPDATE payments SET status = 'paid', paid_at = now()
		WHERE reference = $1 AND status = 'pending'
		RETURNING id, subscription_id, amount_idr, status, reference`,
		reference,
	).Scan(&pay.ID, &pay.SubscriptionID, &pay.AmountIDR, &pay.Status, &pay.Reference)
	if errors.Is(err, pgx.ErrNoRows) {
		return Subscription{}, Payment{}, ErrPaymentFailed
	}
	if err != nil {
		return Subscription{}, Payment{}, err
	}

	var sub Subscription
	err = tx.QueryRow(ctx, `
		UPDATE subscriptions s
		SET status = 'active', starts_at = now(),
		    ends_at = now() + (p.duration_days || ' days')::interval
		FROM plans p
		WHERE s.id = $1 AND s.plan_id = p.id
		RETURNING s.id, s.user_id, s.plan_id, s.status, s.starts_at, s.ends_at`,
		pay.SubscriptionID,
	).Scan(&sub.ID, &sub.UserID, &sub.PlanID, &sub.Status, &sub.StartsAt, &sub.EndsAt)
	if err != nil {
		return Subscription{}, Payment{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Subscription{}, Payment{}, err
	}
	return sub, pay, nil
}

func (r *Repository) ActiveByUser(ctx context.Context, userID string) (Subscription, Plan, error) {
	const q = `
		SELECT s.id, s.user_id, s.plan_id, s.status, s.starts_at, s.ends_at,
		       p.id, p.code, p.name, p.price_idr, p.duration_days, p.max_memes, p.active
		FROM subscriptions s
		JOIN plans p ON p.id = s.plan_id
		WHERE s.user_id = $1 AND s.status = 'active' AND s.ends_at > now()
		ORDER BY s.ends_at DESC LIMIT 1`
	var (
		sub Subscription
		p   Plan
	)
	err := r.pool.QueryRow(ctx, q, userID).Scan(
		&sub.ID, &sub.UserID, &sub.PlanID, &sub.Status, &sub.StartsAt, &sub.EndsAt,
		&p.ID, &p.Code, &p.Name, &p.PriceIDR, &p.Duration, &p.MaxMemes, &p.Active,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Subscription{}, Plan{}, ErrNotSubscribed
	}
	if err != nil {
		return Subscription{}, Plan{}, fmt.Errorf("subscription repository: active by user gagal: %w", err)
	}
	return sub, p, nil
}

// MarkExpired menandai semua langganan aktif yang sudah lewat ends_at
// menjadi 'expired'. Dipanggil janitor periodik; lazy check di
// ActiveByUser (ends_at > now()) tetap menjadi sumber kebenaran runtime.
func (r *Repository) MarkExpired(ctx context.Context) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE subscriptions SET status = 'expired'
		 WHERE status = 'active' AND ends_at <= now()`)
	if err != nil {
		return 0, fmt.Errorf("subscription repository: mark expired gagal: %w", err)
	}
	return tag.RowsAffected(), nil
}

// MemesUsed menghitung meme yang dibuat dalam periode langganan aktif.
func (r *Repository) MemesUsed(ctx context.Context, userID string, since time.Time) (int32, error) {
	var n int32
	err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM memes WHERE user_id = $1 AND created_at >= $2`,
		userID, since,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("subscription repository: memes used gagal: %w", err)
	}
	return n, nil
}
