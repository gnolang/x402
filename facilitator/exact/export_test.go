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

const SettlementTTL = settlementTTL

type Settlements = settlements

func NewSettlements() *Settlements { return newSettlements() }

func (s *Settlements) Claim(hash string, now time.Time) bool { return s.claim(hash, now) }

func (s *Settlements) MarkSettled(hash string, now time.Time) { s.markSettled(hash, now) }
