package payment

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/stripe/stripe-go/v79"
	"github.com/stripe/stripe-go/v79/paymentintent"
)

// Provider abstracts payment-intent creation so tests can run without hitting
// the Stripe network and so local dev works without an API key.
type Provider interface {
	CreateIntent(ctx context.Context, amountCents int64, metadata map[string]string) (intentID, clientSecret string, err error)
	Name() string
}

// StripeProvider talks to the real Stripe API (test mode).
type StripeProvider struct{}

func NewStripeProvider(secretKey string) *StripeProvider {
	stripe.Key = secretKey
	return &StripeProvider{}
}

func (p *StripeProvider) Name() string { return "stripe" }

func (p *StripeProvider) CreateIntent(ctx context.Context, amountCents int64, metadata map[string]string) (string, string, error) {
	params := &stripe.PaymentIntentParams{
		Amount:   stripe.Int64(amountCents),
		Currency: stripe.String(string(stripe.CurrencyUSD)),
		AutomaticPaymentMethods: &stripe.PaymentIntentAutomaticPaymentMethodsParams{
			Enabled: stripe.Bool(true),
		},
	}
	params.Context = ctx
	for k, v := range metadata {
		params.AddMetadata(k, v)
	}
	pi, err := paymentintent.New(params)
	if err != nil {
		return "", "", fmt.Errorf("stripe create intent: %w", err)
	}
	return pi.ID, pi.ClientSecret, nil
}

// MockProvider issues fake payment-intent IDs so the platform is fully runnable
// in local dev / CI without a Stripe key. The webhook path is identical.
type MockProvider struct{}

func NewMockProvider() *MockProvider { return &MockProvider{} }

func (p *MockProvider) Name() string { return "mock" }

func (p *MockProvider) CreateIntent(_ context.Context, _ int64, _ map[string]string) (string, string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	id := "pi_mock_" + hex.EncodeToString(b)
	return id, id + "_secret_" + hex.EncodeToString(b[:6]), nil
}
