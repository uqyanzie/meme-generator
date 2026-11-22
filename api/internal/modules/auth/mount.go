package auth

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nyxsr/meme-portal/api/internal/platform/token"
)

// Mount memasang route modul auth (publik) pada mux.
// Route auth diletakkan publik; endpoint proteksi lain memakai middleware.Auth.
func NewModule(pool *pgxpool.Pool, tokens *token.Manager) *Handler {
	users := NewPostgresRepository(pool)
	return NewHandler(NewService(users, tokens), tokens)
}

func RegisterRoutes(mux *http.ServeMux, h *Handler, authMW func(http.Handler) http.Handler) {
	mux.HandleFunc("POST /api/v1/auth/register", h.Register)
	mux.HandleFunc("POST /api/v1/auth/login", h.Login)
	mux.HandleFunc("POST /api/v1/auth/refresh", h.Refresh)
	mux.Handle("GET /api/v1/auth/me", authMW(http.HandlerFunc(h.Me)))
	mux.HandleFunc("POST /api/v1/auth/logout", h.Logout)
}
