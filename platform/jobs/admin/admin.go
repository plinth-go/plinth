// Package admin exposes a small, explicit HTTP surface for failed-job
// administration. Applications must mount it behind their own authentication
// and authorization middleware.
package admin

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/plinth-go/plinth/kernel"
	"github.com/plinth-go/plinth/platform/jobs"
)

const (
	defaultLimit = 50
	maxLimit     = 100
)

// Store is the failed-job subset used by the admin handler.
type Store = jobs.AdminStore

// NewHandler returns a JSON handler for failed jobs. Mount it beneath an
// authenticated prefix; supported routes are GET /jobs/failed,
// POST /jobs/{id}/retry, and DELETE /jobs/{id}.
func NewHandler(store Store, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	handler := &handler{store: store, logger: logger.With("component", "jobs-admin")}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /jobs/failed", handler.list)
	mux.HandleFunc("POST /jobs/{id}/retry", handler.retry)
	mux.HandleFunc("DELETE /jobs/{id}", handler.delete)
	return securityHeaders(mux)
}

type handler struct {
	store  Store
	logger *slog.Logger
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := pagination(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if h.store == nil {
		h.writeError(w, r, errors.New("job admin store is nil"))
		return
	}
	values, err := h.store.ListFailed(r.Context(), limit, offset)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if values == nil {
		values = []jobs.Job{}
	}
	h.writeJSON(w, http.StatusOK, struct {
		Jobs []jobs.Job `json:"jobs"`
	}{Jobs: values})
}

func (h *handler) retry(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.writeError(w, r, errors.New("job admin store is nil"))
		return
	}
	job, err := h.store.RetryFailed(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, job)
}

func (h *handler) delete(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.writeError(w, r, errors.New("job admin store is nil"))
		return
	}
	if err := h.store.DeleteFailed(r.Context(), r.PathValue("id")); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func pagination(r *http.Request) (int, int, error) {
	limit := defaultLimit
	offset := 0
	query := r.URL.Query()
	if raw := query.Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > maxLimit {
			return 0, 0, kernel.BadRequest("limit must be between 1 and 100")
		}
		limit = value
	}
	if raw := query.Get("offset"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			return 0, 0, kernel.BadRequest("offset must not be negative")
		}
		offset = value
	}
	return limit, offset, nil
}

func (h *handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, jobs.ErrNotFound):
		err = kernel.NotFound("job not found")
	case errors.Is(err, jobs.ErrNotFailed):
		err = kernel.Conflict("job is not in the failed state")
	case errors.Is(err, jobs.ErrInvalidJobID):
		err = kernel.BadRequest("job ID is invalid")
	}
	kernel.WriteProblem(w, r, err, func(message string, args ...any) {
		h.logger.Error(message, args...)
	})
}

func (h *handler) writeJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		h.logger.Error("encode job admin response", "error", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}
