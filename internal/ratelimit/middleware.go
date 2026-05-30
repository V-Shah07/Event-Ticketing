package ratelimit

import (
	"net"
	"net/http"

	"github.com/v-shah07/event-ticketing/internal/auth"
	"github.com/v-shah07/event-ticketing/internal/httpx"
)

// Middleware rejects requests that exceed the limit within the window. It keys
// on the client IP and, when the request is authenticated, additionally on the
// user ID — so a single user cannot burst from many IPs and a single IP cannot
// burst across many accounts.
func (l *Limiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		keys := []string{"ip:" + ip}
		if c := auth.FromContext(r.Context()); c != nil {
			keys = append(keys, "user:"+c.UserID)
		}
		for _, k := range keys {
			ok, err := l.Allow(r.Context(), k)
			if err != nil {
				// Fail open on Redis errors — don't take down writes if the
				// limiter is unavailable.
				continue
			}
			if !ok {
				w.Header().Set("Retry-After", "1")
				httpx.Error(w, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
