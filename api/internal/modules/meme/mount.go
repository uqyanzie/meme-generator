package meme

import (
	"net/http"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nyxsr/meme-portal/api/internal/config"
)

// NewModule menyusun modul meme; gate disuntikkan dari modul subscription.
func NewModule(pool *pgxpool.Pool, s3Client *s3.Client, cfg config.StorageConfig, gate Gate, uploadSecret string) *Handler {
	repo := NewPostgresRepository(pool)
	return NewHandler(NewService(repo, s3Client, cfg, gate, uploadSecret))
}

func RegisterRoutes(mux *http.ServeMux, h *Handler, authMW func(http.Handler) http.Handler) {
	mux.Handle("POST /api/v1/memes/upload", authMW(http.HandlerFunc(h.PrepareUpload)))
	mux.HandleFunc("PUT /api/v1/memes/upload-object", h.UploadObject)
	mux.Handle("POST /api/v1/memes/{id}/result-upload", authMW(http.HandlerFunc(h.ResultUpload)))
	mux.Handle("POST /api/v1/memes/{id}/complete", authMW(http.HandlerFunc(h.Complete)))
	mux.Handle("GET /api/v1/memes", authMW(http.HandlerFunc(h.List)))
	mux.Handle("DELETE /api/v1/memes/{id}", authMW(http.HandlerFunc(h.Delete)))
}
