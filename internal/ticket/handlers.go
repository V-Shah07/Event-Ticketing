package ticket

import (
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/v-shah07/event-ticketing/internal/httpx"
)

type Handler struct {
	Validator *Validator
	QRDir     string
}

func NewHandler(v *Validator, qrDir string) *Handler {
	return &Handler{Validator: v, QRDir: qrDir}
}

type validateRequest struct {
	Token   string `json:"token"`
	Scanner string `json:"scanner"`
}

// Validate is the door-scan endpoint. Concurrent scans of the same token yield
// exactly one "admitted"; the rest are "already_scanned".
func (h *Handler) Validate(w http.ResponseWriter, r *http.Request) {
	var req validateRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Token == "" {
		httpx.Error(w, http.StatusBadRequest, "token required")
		return
	}
	if req.Scanner == "" {
		req.Scanner = "gate"
	}
	res, err := h.Validator.Validate(r.Context(), req.Token, req.Scanner)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	status := http.StatusOK
	switch res.Outcome {
	case InvalidToken, NotFound:
		status = http.StatusNotFound
	case AlreadyScanned:
		status = http.StatusConflict
	}
	httpx.JSON(w, status, map[string]any{
		"outcome":   res.Outcome,
		"admitted":  res.Outcome == Admitted,
		"ticket_id": res.TicketID,
	})
}

// QR serves the stored PNG for a ticket.
func (h *Handler) QR(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	// Guard against path traversal: only allow the bare id as a filename.
	if filepath.Base(id) != id {
		httpx.Error(w, http.StatusBadRequest, "bad id")
		return
	}
	path := filepath.Join(h.QRDir, id+".png")
	f, err := os.Open(path)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "qr not found")
		return
	}
	defer f.Close()
	var mod time.Time
	if fi, statErr := f.Stat(); statErr == nil {
		mod = fi.ModTime()
	}
	w.Header().Set("Content-Type", "image/png")
	http.ServeContent(w, r, id+".png", mod, f)
}
