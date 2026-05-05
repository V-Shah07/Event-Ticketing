// Command api is the core REST/GraphQL/WebSocket API server.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/v-shah07/event-ticketing/internal/analytics"
	"github.com/v-shah07/event-ticketing/internal/auth"
	"github.com/v-shah07/event-ticketing/internal/cache"
	"github.com/v-shah07/event-ticketing/internal/config"
	"github.com/v-shah07/event-ticketing/internal/db"
	"github.com/v-shah07/event-ticketing/internal/payment"
	"github.com/v-shah07/event-ticketing/internal/server"
	"github.com/v-shah07/event-ticketing/internal/ticket"
)

func main() {
	cfg := config.Load()
	ctx := context.Background()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db connect: %v", err)
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	log.Println("migrations applied")

	rdb, err := cache.New(ctx, cfg.RedisAddr)
	if err != nil {
		log.Fatalf("redis connect: %v", err)
	}
	defer rdb.Close()
	log.Println("redis connected")

	// Use real Stripe when a key is present; otherwise a mock provider keeps the
	// platform fully runnable in local dev / CI without a Stripe account.
	var provider payment.Provider
	if cfg.StripeSecretKey != "" {
		provider = payment.NewStripeProvider(cfg.StripeSecretKey)
		log.Println("payments: using Stripe provider")
	} else {
		provider = payment.NewMockProvider()
		log.Println("payments: STRIPE_SECRET_KEY unset, using mock provider")
	}
	payments := payment.NewService(pool, rdb, provider)

	// Connect to the analytics service over gRPC (best-effort; core still works
	// if analytics is down). Purchases are streamed to it after commit.
	if analyticsClient, err := analytics.Dial(cfg.AnalyticsAddr); err != nil {
		log.Printf("analytics dial failed (%v); analytics disabled", err)
	} else {
		defer analyticsClient.Close()
		payments.SetPublisher(analyticsClient)
		log.Printf("analytics client connected to %s", cfg.AnalyticsAddr)
	}

	// Wire QR issuance: minted tickets get a signed token + PNG after commit.
	signer := ticket.NewSigner(cfg.TicketSecret)
	payments.SetTicketIssuer(ticket.NewIssuer(pool, signer, cfg.QRDir))

	handler := server.New(server.Deps{
		Pool:          pool,
		Redis:         rdb,
		JWT:           auth.NewManager(cfg.JWTSecret),
		Payments:      payments,
		StripeWebhKey: cfg.StripeWebhookKey,
		TicketSigner:  signer,
		QRDir:         cfg.QRDir,
	})

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("api listening on %s", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http server: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Println("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
