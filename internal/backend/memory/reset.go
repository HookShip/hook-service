package memory

import (
	"context"
	"time"
)

// Stats is a payload-free snapshot of bounded collection sizes for readiness and
// diagnostics. It never exposes payloads or secrets.
type Stats struct {
	Ledgers  int `json:"ledgers"`
	Nonces   int `json:"nonces"`
	Payloads int `json:"payloads"`
	Ingests  int `json:"ingests"`
	Jobs     int `json:"jobs"`
	Outbox   int `json:"outbox"`
	Attempts int `json:"attempts"`
}

// Stats returns the current bounded collection sizes.
func (b *Backend) Stats() Stats {
	b.mu.Lock()
	defer b.mu.Unlock()
	return Stats{
		Ledgers:  len(b.ledgers),
		Nonces:   len(b.nonces),
		Payloads: len(b.payloads),
		Ingests:  len(b.ingests),
		Jobs:     len(b.jobs),
		Outbox:   len(b.outbox),
		Attempts: len(b.attempts),
	}
}

// Reset clears all ephemeral runtime state while preserving the immutable
// assignment and endpoint-snapshot configuration. It backs the authenticated
// loopback reset endpoint used by test automation and simulates the effect of a
// restart on accepted data.
func (b *Backend) Reset(context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ledgers = make(map[string]*ledgerEntry)
	b.nonces = make(map[string]time.Time)
	b.payloads = make(map[string]*payloadRecord)
	b.ingests = make(map[string]*ingestRecord)
	b.jobs = make(map[string]*jobEntry)
	b.outbox = make(map[string]*outboxEntry)
	b.attempts = nil
	return nil
}
