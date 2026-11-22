package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/nyxsr/meme-portal/api/internal/platform/cookie"
	"github.com/nyxsr/meme-portal/api/internal/platform/token"
)

type ctxKey string

const ctxKeyUserID ctxKey = "userID"

// UserIDFromContext mengembalikan user id yang disisipkan oleh Auth.
func UserIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(ctxKeyUserID).(string)
	return id, ok && id != ""
}

// Auth memvalidasi access token dari header Authorization: Bearer <token>,
// atau fallback ke cookie meme_access_token (sesi berbasis cookie HttpOnly).
func Auth(tokens *token.Manager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Respons endpoint terautentikasi (termasuk 401) berisi atau bergantung pada kredensial user —
			// jangan pernah izinkan shared cache / browser HTTP cache menyimpannya.
			w.Header().Set("Cache-Control", "private, no-store")
			w.Header().Add("Vary", "Cookie")
			w.Header().Add("Vary", "Authorization")

			raw := ""
			if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
				raw = strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
			}
			if raw == "" {
				if c, err := r.Cookie(cookie.AccessTokenName); err == nil {
					raw = c.Value
				}
			}
			if raw == "" {
				unauthorized(w)
				return
			}
			claims, err := tokens.VerifyAccess(r.Context(), raw)
			if err != nil {
				unauthorized(w)
				return
			}
			ctx := context.WithValue(r.Context(), ctxKeyUserID, claims.UserID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"success":false,"code":10002,"message":"unauthorized"}`))
}
