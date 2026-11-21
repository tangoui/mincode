package sqlsafety

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// ErrCheckInvalid means a confirmation token is unknown, already used,
// expired, or presented for a different session, database or query.
var ErrCheckInvalid = errors.New("sqlsafety: check is invalid or expired")

// maxPendingChecks bounds unused tokens; issuing beyond it drops the oldest.
const maxPendingChecks = 256

// Tokens hands out single-use confirmation tokens bound to a Check and the
// hash of the query it assessed. Safe for concurrent use.
type Tokens struct {
	ttl time.Duration
	now func() time.Time

	mu      sync.Mutex
	pending map[string]pendingCheck
	order   []string // issue order of pending IDs, oldest first
}

type pendingCheck struct {
	check     *Check
	queryHash string
}

func NewTokens(ttl time.Duration, now func() time.Time) *Tokens {
	return &Tokens{ttl: ttl, now: now, pending: make(map[string]pendingCheck)}
}

// Issue assigns c a fresh 128-bit CheckID and ExpiresAt and records it.
func (t *Tokens) Issue(c *Check, queryHash string) {
	c.CheckID = randomHex(16)
	c.ExpiresAt = t.now().Add(t.ttl)
	c.queryHash = queryHash

	t.mu.Lock()
	defer t.mu.Unlock()
	t.pending[c.CheckID] = pendingCheck{check: c, queryHash: queryHash}
	t.order = append(t.order, c.CheckID)
	t.compact()
	for len(t.pending) > maxPendingChecks {
		delete(t.pending, t.order[0])
		t.order = t.order[1:]
	}
}

// Reissue issues c again under a fresh CheckID and expiry, bound to the
// same session, database and query hash as the token c (or the check it
// was copied from) was first issued with. Use it to hand back an updated
// assessment of a consumed check. A check never issued is an error.
func (t *Tokens) Reissue(c *Check) error {
	if c.queryHash == "" {
		return errors.New("sqlsafety: check was never issued")
	}
	t.Issue(c, c.queryHash)
	return nil
}

// Consume returns the check for checkID and invalidates the token. It fails
// with ErrCheckInvalid when the token is unknown, used or expired, or when
// the session, database or query hash differ from the issued check; a
// mismatch leaves the token usable by its rightful caller until it expires.
func (t *Tokens) Consume(checkID, sessionID, database, queryHash string) (*Check, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	p, ok := t.pending[checkID]
	if !ok {
		return nil, ErrCheckInvalid
	}
	if t.now().After(p.check.ExpiresAt) {
		delete(t.pending, checkID)
		return nil, ErrCheckInvalid
	}
	if p.check.SessionID != sessionID || p.check.Database != database || p.queryHash != queryHash {
		return nil, ErrCheckInvalid
	}
	delete(t.pending, checkID)
	return p.check, nil
}

// compact drops consumed or expired IDs from order so it stays bounded.
func (t *Tokens) compact() {
	now := t.now()
	kept := t.order[:0]
	for _, id := range t.order {
		p, ok := t.pending[id]
		if ok && now.After(p.check.ExpiresAt) {
			delete(t.pending, id)
			ok = false
		}
		if ok {
			kept = append(kept, id)
		}
	}
	clear(t.order[len(kept):])
	t.order = kept
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b) // crypto/rand.Read never fails (Go 1.24+)
	return hex.EncodeToString(b)
}
