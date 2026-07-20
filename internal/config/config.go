// Package config centralizes environment-driven configuration.
package config

import (
	"os"
)

type Config struct {
	DatabaseURL      string
	RedisAddr        string
	JWTSecret        string
	HTTPAddr         string
	StripeSecretKey  string
	StripeWebhookKey string
	TicketSecret     string
	KafkaBrokers     string
	AnalyticsAddr    string
	GRPCAddr         string
	QRDir            string
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func Load() Config {
	return Config{
		DatabaseURL:      getenv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/ticketing?sslmode=disable"),
		RedisAddr:        getenv("REDIS_ADDR", "localhost:6379"),
		JWTSecret:        getenv("JWT_SECRET", "dev-insecure-jwt-secret-change-me"),
		HTTPAddr:         getenv("HTTP_ADDR", ":8080"),
		StripeSecretKey:  getenv("STRIPE_SECRET_KEY", ""),
		StripeWebhookKey: getenv("STRIPE_WEBHOOK_SECRET", ""),
		TicketSecret:     getenv("TICKET_SECRET", "dev-insecure-ticket-secret-change-me"),
		KafkaBrokers:     getenv("KAFKA_BROKERS", "localhost:9092"),
		AnalyticsAddr:    getenv("ANALYTICS_ADDR", "localhost:9100"),
		GRPCAddr:         getenv("GRPC_ADDR", ":9100"),
		QRDir:            getenv("QR_DIR", "./data/qr"),
	}
}
