package exact

import (
	"sync"
	"time"
)

// settlementTTL is how long a settled payment stays claimed. Once committed, a
// payment's sequence is consumed and an up-to-date node refuses a copy; the
// claim covers a node that has not committed it yet, which would admit a copy
// and answer it with the original's delivery. It equals upstream's
// x402.PendingSettlementTTL.
const settlementTTL = 5 * time.Minute

type settlementState int

const (
	// settling: a settle holds the payment and has not finished.
	settling settlementState = iota
	// settled: a block executed the payment, successfully or not.
	settled
)

type settlement struct {
	state     settlementState
	settledAt time.Time
}

// settlements is the payments this process is settling or has settled, keyed by
// transaction hash, so each is settled once: a copy presented meanwhile is
// refused rather than broadcast through another node and reported settled too.
// It is per process; facilitators that share a payTo share nothing here.
type settlements struct {
	mu      sync.Mutex
	entries map[string]settlement
}

func newSettlements() *settlements {
	return &settlements{entries: make(map[string]settlement)}
}

// claim takes hash for a settle about to run. It reports false when another
// settle holds it, or when it settled within settlementTTL of now.
func (s *settlements) claim(hash string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	// ceiling: a scan per claim; a time-ordered queue if settled payments ever
	// number more than a few thousand per TTL.
	for h, e := range s.entries {
		if e.state == settled && now.Sub(e.settledAt) > settlementTTL {
			delete(s.entries, h)
		}
	}
	if _, held := s.entries[hash]; held {
		return false
	}
	s.entries[hash] = settlement{state: settling}
	return true
}

// release frees a claim whose settle broadcast nothing, so the payment can be
// settled again.
func (s *settlements) release(hash string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, hash)
}

// markSettled records that a block executed the claimed payment at now.
func (s *settlements) markSettled(hash string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[hash] = settlement{state: settled, settledAt: now}
}
