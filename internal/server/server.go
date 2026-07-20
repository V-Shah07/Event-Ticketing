// Package server assembles the HTTP router from the domain handlers. Keeping
// assembly here lets both cmd/api and integration tests build the same routes.
package server

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/v-shah07/event-ticketing/internal/auth"
	"github.com/v-shah07/event-ticketing/internal/event"
	"github.com/v-shah07/event-ticketing/internal/payment"
	"github.com/v-shah07/event-ticketing/internal/user"
)

type Deps struct {
	Pool          *pgxpool.Pool
	Redis         *redis.Client
	JWT           *auth.Manager
	Payments      *payment.Service
	StripeWebhKey string
}

func New(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	r.Get("/healthz", func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	userH := user.NewHandler(user.NewStore(d.Pool), d.JWT)
	r.Post("/auth/register", userH.Register)
	r.Post("/auth/login", userH.Login)

	eventH := event.NewHandler(event.NewStore(d.Pool))

	// Events: reads are public, writes require a valid token (role enforced inside).
	r.Route("/events", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			// Optional auth: attach claims when present so organizers see drafts.
			r.Use(optionalAuth(d.JWT))
			r.Get("/", eventH.List)
			r.Get("/{id}", eventH.Get)
		})
		r.Group(func(r chi.Router) {
			r.Use(d.JWT.Middleware)
			r.Use(auth.RequireRole(auth.RoleOrganizer, auth.RoleAdmin))
			r.Post("/", eventH.Create)
			r.Post("/{id}/tiers", eventH.CreateTier)
			r.Post("/{id}/publish", eventH.Publish)
		})
	})

	// Payments: checkout requires a buyer token; the webhook is called by Stripe.
	if d.Payments != nil {
		payH := payment.NewHandler(d.Payments, d.StripeWebhKey)
		r.Group(func(r chi.Router) {
			r.Use(d.JWT.Middleware)
			r.Post("/checkout", payH.Checkout)
		})
		r.Post("/webhooks/stripe", payH.Webhook)
	}

	return r
}

// optionalAuth attaches claims if a valid token is present but never rejects.
func optionalAuth(m *auth.Manager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			const p = "Bearer "
			h := r.Header.Get("Authorization")
			if len(h) > len(p) && h[:len(p)] == p {
				if claims, err := m.Parse(h[len(p):]); err == nil {
					r = r.WithContext(auth.WithClaims(r.Context(), claims))
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
