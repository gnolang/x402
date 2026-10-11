package exact

import (
	"sync"
	"time"
)

// settlementTTL is how long a settled or pending payment stays recorded. Once
// committed, a payment's sequence is consumed and an up-to-date node refuses a
// copy; the record covers a node that has not committed it yet, which would admit
// a copy and answer it with the original's delivery.
const settlementTTL = 5 * time.Minute

type settlementState int

const (
	unclaimed settlementState = iota
	// settling: a settle holds the payment and has not finished.
	settling
	// settled: a block executed the payment, successfully or not.
	settled
	// pending: the node admitted the payment and no block was seen executing it.
	pending
)

type settlement struct {
	state settlementState
	payer string    // pending: the payer the settle verified
	hash  string    // the hash a node admitted the payment under, once one did
	at    time.Time // settled: when its delivery was seen; pending: its broadcast
}

// settlements is the payments this process is settling or has settled, keyed by
// transaction hash, so each is settled once: a copy presented meanwhile is
// refused rather than broadcast through another node and reported settled too.
// It is per process; two facilitators share nothing here.
type settlements struct {
	mu      sync.Mutex
	entries map[string]settlement
}

func newSettlements() *settlements {
	return &settlements{entries: make(map[string]settlement)}
}

// claim takes key for a settle about to run, and returns what was recorded for
// it before: unclaimed, or pending for a payment to reconcile. It reports false
// when another settle holds the payment or it settled within settlementTTL.
func (s *settlements) claim(key string, now time.Time) (settlement, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// ceiling: a scan per claim; a time-ordered queue if recorded payments ever
	// number more than a few thousand per TTL.
	for k, e := range s.entries {
		if e.state != settling && now.Sub(e.at) > settlementTTL {
			delete(s.entries, k)
		}
	}
	prior := s.entries[key]
	if prior.state == settling || prior.state == settled {
		return prior, false
	}
	// A pending payment keeps its hash and broadcast time while it is reconciled,
	// for a copy refused meanwhile and for the record that follows.
	s.entries[key] = settlement{state: settling, hash: prior.hash, at: prior.at}
	return prior, true
}

// release frees a claim whose settle broadcast nothing, so the payment can be
// settled again.
func (s *settlements) release(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, key)
}

// markSettled records that a block executed the claimed payment, admitted
// under hash.
func (s *settlements) markSettled(key, hash string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[key] = settlement{state: settled, hash: hash, at: now}
}

// markPending records that the node admitted the claimed payment under hash and
// no block was seen executing it, so the next settle of it reconciles. A payment
// already pending keeps the time of its broadcast, so presenting it again does
// not keep it pending past settlementTTL.
func (s *settlements) markPending(key, payer, hash string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	broadcastAt := now
	if since := s.entries[key].at; !since.IsZero() {
		broadcastAt = since
	}
	s.entries[key] = settlement{state: pending, payer: payer, hash: hash, at: broadcastAt}
}
