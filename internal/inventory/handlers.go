package inventory

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/v-shah07/event-ticketing/internal/httpx"
)

type Handler struct {
	Cache *Cache
}

func NewHandler(c *Cache) *Handler { return &Handler{Cache: c} }

// Remaining serves the cached remaining-capacity count for a tier.
func (h *Handler) Remaining(w http.ResponseWriter, r *http.Request) {
	tierID := chi.URLParam(r, "tierID")
	n, cached, err := h.Cache.Remaining(r.Context(), tierID)
	if errors.Is(err, ErrTierNotFound) {
		httpx.Error(w, http.StatusNotFound, "tier not found")
		return
	}
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"tier_id":   tierID,
		"remaining": n,
		"source":    sourceLabel(cached),
	})
}

func sourceLabel(cached bool) string {
	if cached {
		return "cache"
	}
	return "db"
}
