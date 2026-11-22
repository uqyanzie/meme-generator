package subscription

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewModule menyusun modul subscription dari pool database.
func NewModule(pool *pgxpool.Pool) *Handler {
	repo := NewPostgresRepository(pool)
	return NewHandler(NewService(repo, MockGateway{}))
}

func RegisterRoutes(mux *http.ServeMux, h *Handler, authMW func(http.Handler) http.Handler) {
	// Daftar paket publik agar bisa dilihat sebelum login/berlangganan.
	mux.HandleFunc("GET /api/v1/subscription/plans", h.ListPlans)
	mux.Handle("GET /api/v1/subscription/status", authMW(http.HandlerFunc(h.Status)))
	mux.Handle("POST /api/v1/subscription/checkout", authMW(http.HandlerFunc(h.Checkout)))
	// Endpoint mock payment gateway (di produksi: webhook dengan verifikasi signature).
	mux.HandleFunc("POST /api/v1/subscription/payments/{reference}/confirm", h.Confirm)
}
