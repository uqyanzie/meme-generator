// Package v1 memasang seluruh route API versi 1.
package v1

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/nyxsr/meme-portal/api/internal/config"
	"github.com/nyxsr/meme-portal/api/internal/middleware"
	"github.com/nyxsr/meme-portal/api/internal/modules/auth"
	"github.com/nyxsr/meme-portal/api/internal/modules/meme"
	"github.com/nyxsr/meme-portal/api/internal/modules/subscription"
	"github.com/nyxsr/meme-portal/api/internal/platform/token"
)

// subscriptionGate mengadaptasi subscription.Service ke meme.Gate.
type subscriptionGate struct {
	sub *subscription.Service
}

func (g subscriptionGate) HasActiveSubscription(ctx context.Context, userID string) error {
	err := g.sub.HasActive(ctx, userID)
	if errors.Is(err, subscription.ErrNotSubscribed) {
		return meme.ErrNotSubscribedGate
	}
	return err
}

func (g subscriptionGate) CheckQuotaTx(ctx context.Context, tx pgx.Tx, userID string) error {
	err := g.sub.CheckQuotaTx(ctx, tx, userID)
	if errors.Is(err, subscription.ErrNotSubscribed) || errors.Is(err, subscription.ErrQuotaExceeded) {
		return meme.ErrNotSubscribedGate
	}
	return err
}

func Mount(mux *http.ServeMux, pool *pgxpool.Pool, rdb *redis.Client, s3Client *s3.Client, tokens *token.Manager, cfg config.Config) {
	authHandler := auth.NewModule(pool, tokens)
	authMW := middleware.Auth(tokens)

	subHandler := subscription.NewModule(pool)
	subSvc := subscription.NewService(subscription.NewPostgresRepository(pool), subscription.MockGateway{})

	memeHandler := meme.NewModule(pool, s3Client, cfg.Storage, subscriptionGate{sub: subSvc}, cfg.JWT.Secret)

	auth.RegisterRoutes(mux, authHandler, authMW)
	subscription.RegisterRoutes(mux, subHandler, authMW)
	meme.RegisterRoutes(mux, memeHandler, authMW)

	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "code": 0, "message": "ok"})
	})
}
