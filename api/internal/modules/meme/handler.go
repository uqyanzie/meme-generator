package meme

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strconv"

	"github.com/nyxsr/meme-portal/api/internal/middleware"
	"github.com/nyxsr/meme-portal/api/internal/response"
)

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

type prepareUploadDTO struct {
	Title       string `json:"title"`
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	FileSize    *int64 `json:"fileSize"`
	Size        *int64 `json:"size"`
}

func resolveFileSize(fileSize, size *int64) *int64 {
	if fileSize != nil {
		return fileSize
	}
	return size
}

// PrepareUpload: user minta URL upload untuk gambar dasarnya.
func (h *Handler) PrepareUpload(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusUnauthorized, response.CodeUnauthorized, "unauthorized")
		return
	}
	var dto prepareUploadDTO
	if err := json.NewDecoder(r.Body).Decode(&dto); err != nil {
		response.Error(w, http.StatusBadRequest, response.CodeInvalidRequestBody, "invalid request body")
		return
	}
	ext := path.Ext(dto.Filename)
	fileSize := resolveFileSize(dto.FileSize, dto.Size)
	res, err := h.service.PrepareUpload(r.Context(), userID, dto.Title, ext, dto.ContentType, fileSize)
	if err != nil {
		writeError(w, err)
		return
	}
	response.Success(w, http.StatusCreated, CodeUploadPrepared, res, "upload disiapkan")
}

type resultUploadDTO struct {
	FileSize *int64 `json:"fileSize"`
	Size     *int64 `json:"size"`
}

type completeDTO struct {
	ResultURL string `json:"resultUrl"`
}

// ResultUpload: minta presigned PUT URL untuk gambar hasil render canvas.
func (h *Handler) ResultUpload(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusUnauthorized, response.CodeUnauthorized, "unauthorized")
		return
	}
	var dto resultUploadDTO
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&dto); err != nil && !errors.Is(err, io.EOF) {
			response.Error(w, http.StatusBadRequest, response.CodeInvalidRequestBody, "invalid request body")
			return
		}
	}
	fileSize := resolveFileSize(dto.FileSize, dto.Size)
	res, err := h.service.ResultUpload(r.Context(), userID, r.PathValue("id"), fileSize)
	if err != nil {
		writeError(w, err)
		return
	}
	response.Success(w, http.StatusOK, CodeUploadPrepared, res, "upload hasil disiapkan")
}

// Complete: setelah canvas merender & mengunggah hasil ke S3, klien melaporkan URL hasilnya.
func (h *Handler) Complete(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusUnauthorized, response.CodeUnauthorized, "unauthorized")
		return
	}
	id := r.PathValue("id")
	var dto completeDTO
	if err := json.NewDecoder(r.Body).Decode(&dto); err != nil || dto.ResultURL == "" {
		response.Error(w, http.StatusBadRequest, response.CodeInvalidRequestBody, "resultUrl wajib diisi")
		return
	}
	m, err := h.service.Complete(r.Context(), userID, id, dto.ResultURL)
	if err != nil {
		writeError(w, err)
		return
	}
	response.Success(w, http.StatusOK, CodeMemeUpdated, m, "meme selesai")
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusUnauthorized, response.CodeUnauthorized, "unauthorized")
		return
	}
	limit := clampInt(parseQueryInt(r, "limit", defaultLimit), 1, maxLimit)
	offset := max(parseQueryInt(r, "offset", 0), 0)
	res, err := h.service.List(r.Context(), userID, int32(limit), int32(offset))
	if err != nil {
		writeError(w, err)
		return
	}
	response.Success(w, http.StatusOK, CodeMemeListed, res, "daftar meme")
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusUnauthorized, response.CodeUnauthorized, "unauthorized")
		return
	}
	if err := h.service.Delete(r.Context(), userID, r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	response.Success(w, http.StatusOK, CodeMemeDeleted, nil, "meme dihapus")
}

func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidFile):
		response.Error(w, http.StatusBadRequest, response.CodeInvalidImage, err.Error())
	case errors.Is(err, ErrFileTooLarge):
		response.Error(w, http.StatusRequestEntityTooLarge, response.CodeUploadTooLarge, err.Error())
	case errors.Is(err, ErrNotFound):
		response.Error(w, http.StatusNotFound, response.CodeMemeNotFound, err.Error())
	case errors.Is(err, ErrNotSubscribedGate):
		response.Error(w, http.StatusForbidden, response.CodeNotSubscribed, ErrNotSubscribedGate.Error())
	default:
		slog.Error("meme handler: internal error", "err", err)
		response.Error(w, http.StatusInternalServerError, response.CodeInternalServer, "internal server error")
	}
}

func parseQueryInt(r *http.Request, key string, fallback int) int {
	if v := r.URL.Query().Get(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func clampInt(v, lo, hi int) int {
	return min(max(v, lo), hi)
}
