package meme_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nyxsr/meme-portal/api/internal/modules/meme"
)

func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://postgres:postgres@localhost:5432/meme_portal?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("skipping DB test: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("skipping DB test (postgres unreachable): %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func seedTestUser(t *testing.T, pool *pgxpool.Pool, label string) string {
	t.Helper()
	ctx := context.Background()
	email := fmt.Sprintf("tkt107-%s-%d@example.com", label, time.Now().UnixNano())
	var userID string
	err := pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ($1, $2, 'hash') RETURNING id`,
		email, "TKT-107 "+label,
	).Scan(&userID)
	if err != nil {
		t.Fatalf("seed user %s: %v", label, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})
	return userID
}

func TestRepository_GetByID_OwnershipAndScan(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	repo := meme.NewPostgresRepository(pool)

	userA := seedTestUser(t, pool, "userA")
	userB := seedTestUser(t, pool, "userB")

	created, err := repo.Create(ctx, meme.Meme{
		UserID:       userA,
		Title:        "User A Secret",
		BaseImageURL: "http://localhost:3902/meme-portal/base/secret.png",
		TopText:      "TOP",
		BottomText:   "BOTTOM",
		FontSize:     32,
	})
	if err != nil {
		t.Fatalf("create meme: %v", err)
	}

	// Owner fetches own meme -> succeeds with all 9 fields intact.
	got, err := repo.GetByID(ctx, created.ID, userA)
	if err != nil {
		t.Fatalf("owner GetByID failed: %v", err)
	}
	if got.ID != created.ID || got.UserID != userA || got.Title != "User A Secret" {
		t.Fatalf("unexpected meme returned: %+v", got)
	}

	// Non-owner fetches userA's meme -> ErrNotFound.
	_, err = repo.GetByID(ctx, created.ID, userB)
	if !errors.Is(err, meme.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for non-owner, got: %v", err)
	}

	// Malformed UUID -> ErrNotFound.
	_, err = repo.GetByID(ctx, "not-a-uuid", userA)
	if !errors.Is(err, meme.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for malformed UUID, got: %v", err)
	}
}

func TestRepository_SetResultURL_Ownership(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	repo := meme.NewPostgresRepository(pool)

	userA := seedTestUser(t, pool, "userA")
	userB := seedTestUser(t, pool, "userB")

	created, err := repo.Create(ctx, meme.Meme{
		UserID:       userA,
		Title:        "User A Meme",
		BaseImageURL: "http://localhost:3902/meme-portal/base/a.png",
	})
	if err != nil {
		t.Fatalf("create meme: %v", err)
	}

	// Non-owner attempts to update -> ErrNotFound and DB row remains unmodified.
	_, err = repo.SetResultURL(ctx, created.ID, userB, "http://evil.example/hijack.png")
	if !errors.Is(err, meme.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for non-owner SetResultURL, got: %v", err)
	}

	var resultURL *string
	if err := pool.QueryRow(ctx, `SELECT result_url FROM memes WHERE id = $1`, created.ID).Scan(&resultURL); err != nil {
		t.Fatalf("verify result_url untouched: %v", err)
	}
	if resultURL != nil {
		t.Fatalf("expected result_url to remain NULL, got %q", *resultURL)
	}

	// Owner updates own meme -> succeeds and persists result_url.
	wantURL := "http://localhost:3902/meme-portal/result/" + created.ID + ".png"
	updated, err := repo.SetResultURL(ctx, created.ID, userA, wantURL)
	if err != nil {
		t.Fatalf("owner SetResultURL failed: %v", err)
	}
	if updated.ResultURL == nil || *updated.ResultURL != wantURL {
		t.Fatalf("expected result_url %q, got %v", wantURL, updated.ResultURL)
	}
}

func TestRepository_Delete_Ownership(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	repo := meme.NewPostgresRepository(pool)

	userA := seedTestUser(t, pool, "userA")
	userB := seedTestUser(t, pool, "userB")

	created, err := repo.Create(ctx, meme.Meme{
		UserID:       userA,
		Title:        "User A Meme",
		BaseImageURL: "http://localhost:3902/meme-portal/base/a.png",
	})
	if err != nil {
		t.Fatalf("create meme: %v", err)
	}

	// Non-owner attempts to delete -> ErrNotFound and meme still exists.
	_, err = repo.Delete(ctx, created.ID, userB)
	if !errors.Is(err, meme.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for non-owner Delete, got: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM memes WHERE id = $1`, created.ID).Scan(&count); err != nil {
		t.Fatalf("check meme still exists: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected meme to still exist, count=%d", count)
	}

	// Owner deletes own meme -> succeeds, returns deleted Meme, and row is removed.
	deleted, err := repo.Delete(ctx, created.ID, userA)
	if err != nil {
		t.Fatalf("owner Delete failed: %v", err)
	}
	if deleted.ID != created.ID || deleted.BaseImageURL != created.BaseImageURL {
		t.Fatalf("unexpected deleted meme metadata: %+v", deleted)
	}

	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM memes WHERE id = $1`, created.ID).Scan(&count); err != nil {
		t.Fatalf("check meme removed: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected meme to be removed, count=%d", count)
	}
}
