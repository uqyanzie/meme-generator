package auth

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/nyxsr/meme-portal/api/internal/middleware"
	"github.com/nyxsr/meme-portal/api/internal/platform/cookie"
	"github.com/nyxsr/meme-portal/api/internal/platform/token"
	"github.com/nyxsr/meme-portal/api/internal/response"
)

type Handler struct {
	service *Service
	tokens  *token.Manager
}

func NewHandler(service *Service, tokens *token.Manager) *Handler {
	return &Handler{service: service, tokens: tokens}
}

// setAuthCookies menyimpan token ke cookie HttpOnly agar semua request
// berikutnya membawa kredensial tanpa menyimpan token di localStorage.
func setAuthCookies(w http.ResponseWriter, res AuthResponse) {
	cookie.Set(w, cookie.AccessTokenName, res.Token.AccessToken,
		int(res.Token.ExpiresIn), false)
	// Refresh TTL default 720h (30 hari) — samakan dengan config JWT.
	cookie.Set(w, cookie.RefreshTokenName, res.Token.RefreshToken,
		30*24*3600, false)
}

// clearAuthCookies menghapus cookie auth pada Path "/" agar cocok dengan setAuthCookies.
func clearAuthCookies(w http.ResponseWriter) {
	cookie.Clear(w, cookie.AccessTokenName)
	cookie.Clear(w, cookie.RefreshTokenName)
}

func extractAccessToken(r *http.Request) string {
	if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
		if raw := strings.TrimSpace(strings.TrimPrefix(header, "Bearer ")); raw != "" {
			return raw
		}
	}
	if c, err := r.Cookie(cookie.AccessTokenName); err == nil {
		return c.Value
	}
	return ""
}

func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	var dto RegisterDTO
	if err := json.NewDecoder(r.Body).Decode(&dto); err != nil {
		response.Error(w, http.StatusBadRequest, response.CodeInvalidRequestBody, "invalid request body")
		return
	}
	if dto.Email == "" || dto.Password == "" {
		response.ErrorWith(w, http.StatusBadRequest, response.CodeInvalidRequestBody, "validasi gagal", map[string]any{
			"email":    "wajib diisi",
			"password": "wajib diisi",
		})
		return
	}

	res, err := h.service.Register(r.Context(), dto)
	if err != nil {
		writeError(w, err)
		return
	}
	setAuthCookies(w, res)
	response.Success(w, http.StatusCreated, CodeAuthRegisterSuccess, res, "registrasi berhasil")
}

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var dto LoginDTO
	if err := json.NewDecoder(r.Body).Decode(&dto); err != nil {
		response.Error(w, http.StatusBadRequest, response.CodeInvalidRequestBody, "invalid request body")
		return
	}

	res, err := h.service.Login(r.Context(), dto)
	if err != nil {
		writeError(w, err)
		return
	}
	setAuthCookies(w, res)
	response.Success(w, http.StatusOK, CodeAuthLoginSuccess, res, "login berhasil")
}

func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	var dto RefreshDTO
	if c, err := r.Cookie(cookie.RefreshTokenName); err == nil {
		dto.RefreshToken = c.Value
	} else if err := json.NewDecoder(r.Body).Decode(&dto); err != nil {
		response.Error(w, http.StatusBadRequest, response.CodeInvalidRequestBody, "invalid request body")
		return
	}

	res, err := h.service.Refresh(r.Context(), dto.RefreshToken)
	if err != nil {
		writeError(w, err)
		return
	}
	setAuthCookies(w, res)
	response.Success(w, http.StatusOK, CodeAuthTokenRefreshed, res, "token diperbarui")
}

func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusUnauthorized, response.CodeUnauthorized, "unauthorized")
		return
	}
	user, err := h.service.Me(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	response.Success(w, http.StatusOK, CodeAuthMeSuccess, user, "profil user")
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	accessRaw := extractAccessToken(r)
	refreshRaw := ""
	if c, err := r.Cookie(cookie.RefreshTokenName); err == nil {
		refreshRaw = c.Value
	}

	if err := h.service.Logout(r.Context(), accessRaw, refreshRaw); err != nil {
		slog.WarnContext(r.Context(), "auth: gagal mencabut token saat logout", "err", err)
	}
	clearAuthCookies(w)
	response.Success(w, http.StatusOK, CodeAuthLogoutSuccess, nil, "logout berhasil")
}

func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrEmailTaken):
		response.Error(w, http.StatusConflict, response.CodeEmailTaken, err.Error())
	case errors.Is(err, ErrInvalidCredential):
		response.Error(w, http.StatusUnauthorized, response.CodeInvalidCredential, err.Error())
	case errors.Is(err, ErrInvalidToken):
		response.Error(w, http.StatusUnauthorized, response.CodeUnauthorized, err.Error())
	case errors.Is(err, ErrUserNotFound):
		response.Error(w, http.StatusNotFound, response.CodeNotFound, err.Error())
	default:
		response.Error(w, http.StatusInternalServerError, response.CodeInternalServer, "internal server error")
	}
}
