package exact

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
