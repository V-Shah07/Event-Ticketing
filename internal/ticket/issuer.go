package ticket

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5/pgxpool"
	qrcode "github.com/skip2/go-qrcode"
)

// Issuer generates a signed token and a QR PNG for each freshly minted ticket.
// The "email" step is stubbed: it logs a link to the stored PNG (real SES is a
// nice-to-have, not required).
type Issuer struct {
	pool   *pgxpool.Pool
	signer Signer
	qrDir  string
}

func NewIssuer(pool *pgxpool.Pool, signer Signer, qrDir string) *Issuer {
	return &Issuer{pool: pool, signer: signer, qrDir: qrDir}
}

// IssueForPurchase assigns a signed token + QR PNG to every ticket of a purchase
// that does not yet have one. Safe to call more than once (idempotent per
// ticket via the WHERE qr_token IS NULL guard).
func (i *Issuer) IssueForPurchase(ctx context.Context, purchaseID string) error {
	rows, err := i.pool.Query(ctx,
		`SELECT id FROM tickets WHERE purchase_id=$1 AND qr_token IS NULL`, purchaseID)
	if err != nil {
		return fmt.Errorf("select tickets: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	if err := os.MkdirAll(i.qrDir, 0o755); err != nil {
		return fmt.Errorf("mkdir qr dir: %w", err)
	}

	for _, id := range ids {
		token := i.signer.Token(id)
		if _, err := i.pool.Exec(ctx,
			`UPDATE tickets SET qr_token=$1 WHERE id=$2`, token, id); err != nil {
			return fmt.Errorf("store token: %w", err)
		}
		path := filepath.Join(i.qrDir, id+".png")
		if err := qrcode.WriteFile(token, qrcode.Medium, 256, path); err != nil {
			return fmt.Errorf("write qr png: %w", err)
		}
		// "Email": stub that logs the retrievable link (SES optional).
		log.Printf("ticket %s: QR issued -> link=/tickets/%s/qr.png", id, id)
	}
	return nil
}
