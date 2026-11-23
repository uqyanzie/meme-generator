package meme

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func isInvalidUUID(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "22P02"
}

type Repository struct {
	pool *pgxpool.Pool
}

func NewPostgresRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) Create(ctx context.Context, m Meme) (Meme, error) {
	const q = `
		INSERT INTO memes (user_id, title, base_url, top_text, bottom_text, font_size)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, user_id, title, base_url, result_url, top_text, bottom_text, font_size, created_at`
	var out Meme
	err := r.pool.QueryRow(ctx, q,
		m.UserID, m.Title, m.BaseImageURL, m.TopText, m.BottomText, m.FontSize,
	).Scan(&out.ID, &out.UserID, &out.Title, &out.BaseImageURL, &out.ResultURL,
		&out.TopText, &out.BottomText, &out.FontSize, &out.CreatedAt)
	if err != nil {
		return Meme{}, fmt.Errorf("meme repository: create gagal: %w", err)
	}
	return out, nil
}

// CreateWithCheck menjalankan fungsi validasi/kuota dan INSERT meme dalam satu
// transaksi PostgreSQL sehingga pengecekan kuota dan pembuatan baris bersifat atomik.
func (r *Repository) CreateWithCheck(ctx context.Context, m Meme, check func(ctx context.Context, tx pgx.Tx) error) (Meme, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Meme{}, fmt.Errorf("meme repository: begin tx gagal: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if check != nil {
		if err := check(ctx, tx); err != nil {
			return Meme{}, err
		}
	}

	const q = `
		INSERT INTO memes (user_id, title, base_url, top_text, bottom_text, font_size)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, user_id, title, base_url, result_url, top_text, bottom_text, font_size, created_at`
	var out Meme
	err = tx.QueryRow(ctx, q,
		m.UserID, m.Title, m.BaseImageURL, m.TopText, m.BottomText, m.FontSize,
	).Scan(&out.ID, &out.UserID, &out.Title, &out.BaseImageURL, &out.ResultURL,
		&out.TopText, &out.BottomText, &out.FontSize, &out.CreatedAt)
	if err != nil {
		return Meme{}, fmt.Errorf("meme repository: create with check gagal: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Meme{}, fmt.Errorf("meme repository: commit gagal: %w", err)
	}
	return out, nil
}

func (r *Repository) GetByID(ctx context.Context, id, userID string) (Meme, error) {
	const q = `
		SELECT id, user_id, title, base_url, result_url, top_text, bottom_text, font_size, created_at
		FROM memes
		WHERE id = $1 AND user_id = $2`
	var m Meme
	err := r.pool.QueryRow(ctx, q, id, userID).Scan(
		&m.ID, &m.UserID, &m.Title, &m.BaseImageURL, &m.ResultURL,
		&m.TopText, &m.BottomText, &m.FontSize, &m.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) || isInvalidUUID(err) {
		return Meme{}, ErrNotFound
	}
	if err != nil {
		return Meme{}, fmt.Errorf("meme repository: get by id gagal: %w", err)
	}
	return m, nil
}

func (r *Repository) SetResultURL(ctx context.Context, id, userID, resultURL string) (Meme, error) {
	const q = `
		UPDATE memes SET result_url = $2, updated_at = now()
		WHERE id = $1 AND user_id = $3
		RETURNING id, user_id, title, base_url, result_url, top_text, bottom_text, font_size, created_at`
	var out Meme
	err := r.pool.QueryRow(ctx, q, id, resultURL, userID).Scan(
		&out.ID, &out.UserID, &out.Title, &out.BaseImageURL, &out.ResultURL,
		&out.TopText, &out.BottomText, &out.FontSize, &out.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) || isInvalidUUID(err) {
		return Meme{}, ErrNotFound
	}
	if err != nil {
		return Meme{}, fmt.Errorf("meme repository: set result gagal: %w", err)
	}
	return out, nil
}

func (r *Repository) ListByUser(ctx context.Context, userID string, limit, offset int32) ([]Meme, int64, error) {
	var total int64
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM memes WHERE user_id = $1`, userID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("meme repository: count gagal: %w", err)
	}
	const q = `
		SELECT id, user_id, title, base_url, result_url, top_text, bottom_text, font_size, created_at
		FROM memes WHERE user_id = $1
		ORDER BY created_at DESC LIMIT $2 OFFSET $3`
	rows, err := r.pool.Query(ctx, q, userID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("meme repository: list gagal: %w", err)
	}
	defer rows.Close()
	var items []Meme
	for rows.Next() {
		var m Meme
		if err := rows.Scan(&m.ID, &m.UserID, &m.Title, &m.BaseImageURL, &m.ResultURL,
			&m.TopText, &m.BottomText, &m.FontSize, &m.CreatedAt); err != nil {
			return nil, 0, err
		}
		items = append(items, m)
	}
	return items, total, rows.Err()
}

func (r *Repository) Delete(ctx context.Context, id, userID string) (Meme, error) {
	const q = `
		DELETE FROM memes
		WHERE id = $1 AND user_id = $2
		RETURNING id, user_id, title, base_url, result_url, top_text, bottom_text, font_size, created_at`
	var deleted Meme
	err := r.pool.QueryRow(ctx, q, id, userID).Scan(
		&deleted.ID, &deleted.UserID, &deleted.Title, &deleted.BaseImageURL, &deleted.ResultURL,
		&deleted.TopText, &deleted.BottomText, &deleted.FontSize, &deleted.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) || isInvalidUUID(err) {
		return Meme{}, ErrNotFound
	}
	if err != nil {
		return Meme{}, fmt.Errorf("meme repository: delete gagal: %w", err)
	}
	return deleted, nil
}
