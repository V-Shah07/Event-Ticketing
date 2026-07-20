package event

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/v-shah07/event-ticketing/internal/auth"
	"github.com/v-shah07/event-ticketing/internal/httpx"
)

type Handler struct {
	Store *Store
}

func NewHandler(s *Store) *Handler { return &Handler{Store: s} }

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	claims := auth.FromContext(r.Context())
	var in CreateEventInput
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if in.Title == "" {
		httpx.Error(w, http.StatusBadRequest, "title required")
		return
	}
	e, err := h.Store.Create(r.Context(), claims.UserID, in)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, http.StatusCreated, e)
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	e, err := h.Store.Get(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "event not found")
		return
	}
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, e)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	// Anonymous / buyer callers only see published events; organizers see all.
	onlyPublished := true
	if c := auth.FromContext(r.Context()); c != nil && (c.Role == auth.RoleOrganizer || c.Role == auth.RoleAdmin) {
		onlyPublished = false
	}
	events, err := h.Store.List(r.Context(), onlyPublished)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	if events == nil {
		events = []Event{}
	}
	httpx.JSON(w, http.StatusOK, events)
}

func (h *Handler) CreateTier(w http.ResponseWriter, r *http.Request) {
	claims := auth.FromContext(r.Context())
	eventID := chi.URLParam(r, "id")
	owns, err := h.Store.OwnsEvent(r.Context(), eventID, claims.UserID)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "event not found")
		return
	}
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !owns && claims.Role != auth.RoleAdmin {
		httpx.Error(w, http.StatusForbidden, "not your event")
		return
	}
	var in CreateTierInput
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if in.Name == "" || in.Capacity < 0 || in.PriceCents < 0 {
		httpx.Error(w, http.StatusBadRequest, "name, non-negative capacity and price required")
		return
	}
	t, err := h.Store.CreateTier(r.Context(), eventID, in)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, http.StatusCreated, t)
}

func (h *Handler) Publish(w http.ResponseWriter, r *http.Request) {
	claims := auth.FromContext(r.Context())
	eventID := chi.URLParam(r, "id")
	err := h.Store.Publish(r.Context(), eventID, claims.UserID)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "event not found or not yours")
		return
	}
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "published"})
}
