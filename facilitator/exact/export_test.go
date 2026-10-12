package exact

import "time"

// The internals the external tests in exact_test check directly.
var (
	VerifySignature     = verifySignature
	VerifyAccountChecks = verifyAccountChecks
	Memo                = memo
	ChainRefused        = chainRefused
)

const (
	SignatureFresh        = signatureFresh
	SignatureConsumed     = signatureConsumed
	SignatureUnverifiable = signatureUnverifiable

	PaymentAccepted = paymentAccepted
	PaymentRefused  = paymentRefused
	NoVerdict       = noVerdict
)

const (
	SettlementTTL  = settlementTTL
	DeliveryMargin = deliveryMargin
)

type Settlements = settlements

func NewSettlements() *Settlements { return newSettlements() }

// Claim reports whether hash was claimed, and whether it was pending before.
func (s *Settlements) Claim(hash string, now time.Time) (wasPending, ok bool) {
	prior, ok := s.claim(hash, now)
	return prior.state == pending, ok
}

func (s *Settlements) MarkSettled(hash string, now time.Time) { s.markSettled(hash, hash, now) }

func (s *Settlements) MarkPending(hash string, now time.Time) {
	s.markPending(hash, "g1payer", hash, now)
}
