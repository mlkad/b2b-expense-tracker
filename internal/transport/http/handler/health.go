package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/mlkad/b2b-expense-tracker/internal/platform/postgres"
)

type HealthHandler struct {
	db      *postgres.DB
	version string
	started time.Time
}

func NewHealthHandler(db *postgres.DB, version string) *HealthHandler {
	return &HealthHandler{db: db, version: version, started: time.Now()}
}

// Live answers whether the process is running. It touches nothing, because a
// liveness probe that checks a dependency restarts the process when the
// dependency fails - turning a database blip into a rolling restart of every
// replica at once.
func (h *HealthHandler) Live(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": h.version,
		"uptime":  time.Since(h.started).Round(time.Second).String(),
	})
}

// Ready answers whether this replica should receive traffic. It does check the
// database, because a replica that cannot reach it can serve nothing.
func (h *HealthHandler) Ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	if err := h.db.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status":   "unready",
			"database": "unreachable",
		})
		return
	}

	stat := h.db.Stat()
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": h.version,
		"database": map[string]any{
			"acquired":    stat.AcquiredConns(),
			"idle":        stat.IdleConns(),
			"total":       stat.TotalConns(),
			"max":         stat.MaxConns(),
			"acquire_ms":  stat.AcquireDuration().Milliseconds(),
			"empty_waits": stat.EmptyAcquireCount(),
		},
	})
}
