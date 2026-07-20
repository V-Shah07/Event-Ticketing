// Package auth handles password hashing, JWT issuance/verification, and the
// HTTP middleware that enforces role-based access.
package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

type Role string

const (
	RoleOrganizer Role = "organizer"
	RoleBuyer     Role = "buyer"
	RoleAdmin     Role = "admin"
)

type ctxKey string

const claimsKey ctxKey = "claims"

// Claims is the JWT payload.
type Claims struct {
	UserID string `json:"uid"`
	Role   Role   `json:"role"`
	jwt.RegisteredClaims
}

// Manager issues and verifies tokens with a shared secret.
type Manager struct {
	secret []byte
	ttl    time.Duration
}

func NewManager(secret string) *Manager {
	return &Manager{secret: []byte(secret), ttl: 24 * time.Hour}
}

func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

func (m *Manager) Issue(userID string, role Role) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID: userID,
		Role:   role,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(m.ttl)),
			Subject:   userID,
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.secret)
}

func (m *Manager) Parse(tokenStr string) (*Claims, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return m.secret, nil
	})
	if err != nil {
		return nil, err
	}
	return claims, nil
}

// Middleware validates the bearer token and stores claims in the request context.
func (m *Manager) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}
		claims, err := m.Parse(strings.TrimPrefix(header, "Bearer "))
		if err != nil {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), claimsKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireRole wraps a handler so only the given roles may proceed.
func RequireRole(roles ...Role) func(http.Handler) http.Handler {
	allowed := make(map[Role]bool, len(roles))
	for _, r := range roles {
		allowed[r] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c := FromContext(r.Context())
			if c == nil || !allowed[c.Role] {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// FromContext returns the claims stored by Middleware, or nil.
func FromContext(ctx context.Context) *Claims {
	c, _ := ctx.Value(claimsKey).(*Claims)
	return c
}

// WithClaims returns a context carrying the given claims.
func WithClaims(ctx context.Context, c *Claims) context.Context {
	return context.WithValue(ctx, claimsKey, c)
}
