package payment

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/stripe/stripe-go/v79"
	"github.com/stripe/stripe-go/v79/webhook"
	"github.com/v-shah07/event-ticketing/internal/auth"
	"github.com/v-shah07/event-ticketing/internal/httpx"
)

type Handler struct {
	Svc          *Service
	WebhookKey   string // Stripe signing secret; empty disables verification (dev/test)
	maxWebhookKB int64
}

func NewHandler(svc *Service, webhookKey string) *Handler {
	return &Handler{Svc: svc, WebhookKey: webhookKey, maxWebhookKB: 64}
}

// Checkout is buyer-facing: it creates a payment intent for a tier.
func (h *Handler) Checkout(w http.ResponseWriter, r *http.Request) {
	claims := auth.FromContext(r.Context())
	if claims == nil {
		httpx.Error(w, http.StatusUnauthorized, "auth required")
		return
	}
	var in CheckoutInput
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	if in.TierID == "" {
		httpx.Error(w, http.StatusBadRequest, "tier_id required")
		return
	}
	res, err := h.Svc.Checkout(r.Context(), claims.UserID, in)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

// stripeEvent is the minimal shape we read from a webhook payload.
type stripeEvent struct {
	Type string `json:"type"`
	Data struct {
		Object struct {
			ID string `json:"id"`
		} `json:"object"`
	} `json:"data"`
}

// Webhook is the Stripe endpoint. It verifies the signature when a signing
// secret is configured, then idempotently processes payment_intent.succeeded.
func (h *Handler) Webhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, h.maxWebhookKB*1024))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "read body")
		return
	}
	defer r.Body.Close()

	var evt stripeEvent
	if h.WebhookKey != "" {
		// Verify the Stripe signature and use the verified event.
		se, verr := webhook.ConstructEvent(body, r.Header.Get("Stripe-Signature"), h.WebhookKey)
		if verr != nil {
			httpx.Error(w, http.StatusBadRequest, "signature verification failed")
			return
		}
		evt.Type = string(se.Type)
		var pi stripe.PaymentIntent
		if err := json.Unmarshal(se.Data.Raw, &pi); err == nil {
			evt.Data.Object.ID = pi.ID
		}
	} else {
		// Dev/test mode: trust the payload as-is.
		if err := json.Unmarshal(body, &evt); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid json")
			return
		}
	}

	if evt.Type != "payment_intent.succeeded" {
		// Acknowledge unrelated events without doing anything.
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ignored", "type": evt.Type})
		return
	}
	if evt.Data.Object.ID == "" {
		httpx.Error(w, http.StatusBadRequest, "missing payment intent id")
		return
	}

	res, err := h.Svc.ProcessPaymentSucceeded(r.Context(), evt.Data.Object.ID)
	if errors.Is(err, ErrPurchaseNotFound) {
		// Tell Stripe to retry — checkout may not have committed yet.
		httpx.Error(w, http.StatusServiceUnavailable, "purchase not yet available")
		return
	}
	if errors.Is(err, ErrSoldOut) {
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "sold_out"})
		return
	}
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"status":         "ok",
		"created":        res.Created,
		"already_done":   res.AlreadyDone,
		"tickets_minted": res.TicketsMinted,
	})
}
