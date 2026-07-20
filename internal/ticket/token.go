// Package ticket handles signed QR tokens, QR-code generation, and the
// exactly-once door-scan validation backed by a Redis SETNX distributed lock.
package ticket

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strings"
)

// Signer produces and verifies tamper-proof ticket tokens. A token is
// `<ticketID>.<base64url(HMAC-SHA256(ticketID))>`; the HMAC means a scanner (or
// an attacker) cannot forge a token for a ticket ID they don't already hold a
// valid signature for.
type Signer struct {
	secret []byte
}

func NewSigner(secret string) Signer { return Signer{secret: []byte(secret)} }

func (s Signer) sign(ticketID string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(ticketID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Token returns the signed token for a ticket ID.
func (s Signer) Token(ticketID string) string {
	return ticketID + "." + s.sign(ticketID)
}

// Verify checks a token's signature and returns the ticket ID it authorizes.
func (s Signer) Verify(token string) (ticketID string, ok bool) {
	i := strings.LastIndexByte(token, '.')
	if i <= 0 || i == len(token)-1 {
		return "", false
	}
	id, sig := token[:i], token[i+1:]
	expected := s.sign(id)
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return "", false
	}
	return id, true
}
