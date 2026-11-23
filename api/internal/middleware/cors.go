package middleware

// CORS middleware: wajib agar browser (localhost:3000) boleh memanggil API
// lintas origin (localhost:8080) termasuk preflight OPTIONS — tanpa ini,
// preflight ditolak 405 dan axios hanya melaporkan "Network Error".

import (
	"net/http"
	"strings"
)

func CORS(allowedOrigins string) func(http.Handler) http.Handler {
	origins := map[string]bool{}
	for _, o := range strings.Split(allowedOrigins, ",") {
		if o = strings.TrimSpace(o); o != "" {
			origins[o] = true
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && origins[origin] {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Vary", "Origin")
				h.Set("Access-Control-Allow-Credentials", "true")
				h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				h.Set("Access-Control-Allow-Headers",
					"Content-Type, Authorization, X-Requested-With, Idempotency-Key")
				h.Set("Access-Control-Max-Age", "86400")
			}
			// Jawab preflight langsung, jangan sampai jatuh ke route handler
			// yang tidak punya pattern OPTIONS (bisa 405).
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
