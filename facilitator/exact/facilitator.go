// Package exact is the facilitator half of gno's "exact" x402 mechanism: an
// x402.SchemeNetworkFacilitator that verifies a payment and settles it on one gno
// chain. Register it on upstream's facilitator engine to serve gno:
//
//	x402.Newx402Facilitator().Register(
//		[]x402.Network{"gno:onyx-1"}, exact.NewFacilitator(node, "onyx-1"))
//
// It holds no key. A payment's payload is a fully signed, unbroadcast bank send;
// this verifies it offline and against the signer's on-chain account, then relays
// it. It can therefore move funds nowhere the payer did not already sign for.
package exact

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"

	abci "github.com/gnolang/gno/tm2/pkg/bft/abci/types"
	"github.com/gnolang/gno/tm2/pkg/std"
	x402 "github.com/x402-foundation/x402/go/v2"
	"github.com/x402-foundation/x402/go/v2/types"
)

const schemeExact = "exact"

// caipFamily is the CAIP-2 namespace every gno network shares.
const caipFamily = "gno:*"

// AssetTransferMethod names how a gno payment moves value: the payer signs a
// bank send the facilitator broadcasts. It is the scheme's default, so an offer
// that omits extra.assetTransferMethod means it.
const AssetTransferMethod = "bank-send"

// MaxMemoBytes caps extra.memo; a longer memo would bloat the signed tx and every
// echo of the requirements. gno's own limit is far higher, so the cap is the
// scheme's rather than the chain's — and the number is the ecosystem's, not one
// picked here: the shipped SVM mechanism caps its own extra.memo at the same 256
// bytes (mechanisms/svm/constants.go), and its spec states the field's purpose in
// the same terms, attaching a payment reference such as an invoice id for
// reconciliation without needing a unique deposit address per payer.
//
// It is exported because both halves of the mechanism answer for it: the
// facilitator refuses an over-cap memo on every payment, and the seller has to
// refuse one while the requirements are still its own to fix. One name keeps the
// two from drifting apart.
const MaxMemoBytes = 256

// Node is the chain access the facilitator needs: the signer's account for
// signature and sequence checks, full-tx simulation for verify, commit broadcast
// for settle. Implemented by the gnoclient adapter NewGnoclientNode and by fakes
// in tests.
//
// SignerAccount and Simulate each report a refusal the chain decided and a
// failure to reach the chain at all through one error, so an implementation has
// to preserve what it was handed: chainRefused tells the two apart by the
// chain's own abci.Error, and only that is a verdict on the payment.
type Node interface {
	AccountReader
	Simulate(tx *std.Tx) error

	// Broadcast relays tx, the signed transaction's bytes exactly as the payer
	// encoded them.
	Broadcast(tx []byte) (hash string, err error)
}

// NoVerdictError reports a payment the facilitator could not decide: the chain it
// checks against could not be read, or the request ended before a broadcast. It
// carries no reason code, because nothing was established about the payment.
type NoVerdictError struct {
	// Payer is the address the payment's transaction claims to pay from, whether
	// or not its signature verified, or empty when the transaction did not decode.
	// It is for the operator's log; no answer carries it.
	Payer string
	Err   error
}

func (e *NoVerdictError) Error() string { return "no verdict on the payment: " + e.Err.Error() }

func (e *NoVerdictError) Unwrap() error { return e.Err }

// Facilitator verifies and settles gno "exact" payments for one chain.
type Facilitator struct {
	node    Node
	chainID string
}

var _ x402.SchemeNetworkFacilitator = (*Facilitator)(nil)

// NewFacilitator returns the mechanism for the chain whose id is chainID, the
// reference half of the network "gno:<chainID>". The chain id is expected to
// satisfy ValidChainID.
func NewFacilitator(node Node, chainID string) *Facilitator {
	return &Facilitator{node: node, chainID: chainID}
}

// Scheme names the x402 payment scheme this implements.
func (f *Facilitator) Scheme() string { return schemeExact }

// CaipFamily is the CAIP-2 family the engine groups this facilitator's signers
// under.
func (f *Facilitator) CaipFamily() string { return caipFamily }

// GetExtra is nil: a gno payment needs nothing beyond the requirements to build.
func (f *Facilitator) GetExtra(x402.Network) map[string]interface{} { return nil }

// GetSigners is empty: the facilitator holds no key, and the payer signs and pays
// the fee of the transaction it relays.
func (f *Facilitator) GetSigners(x402.Network) []string { return nil }

func (f *Facilitator) network() string { return "gno:" + f.chainID }

// Verify reports whether a payment would settle, without writing to the chain. A
// refusal is an *x402.VerifyError carrying the reason code; a payment the chain
// could not be read for is a *NoVerdictError.
func (f *Facilitator) Verify(ctx context.Context, payload types.PaymentPayload, requirements types.PaymentRequirements, _ *x402.FacilitatorContext) (*x402.VerifyResponse, error) {
	check := f.check(ctx, payload, requirements)
	switch check.verdict {
	case noVerdict:
		return nil, &NoVerdictError{Payer: check.payer, Err: check.cause}
	case paymentRefused:
		return nil, x402.NewVerifyError(check.reason, check.payer, causeText(check.cause))
	}
	return &x402.VerifyResponse{IsValid: true, Payer: check.payer}, nil
}

// Settle verifies a payment again and broadcasts it. A refusal is an
// *x402.SettleError carrying the reason code; a payment the chain could not be
// read for, or whose request ended before the broadcast, is a *NoVerdictError
// and moved nothing.
//
// A delivery the chain committed and then aborted charged the payer its fee, so
// its refusal carries the transaction's hash.
func (f *Facilitator) Settle(ctx context.Context, payload types.PaymentPayload, requirements types.PaymentRequirements, _ *x402.FacilitatorContext) (*x402.SettleResponse, error) {
	check := f.check(ctx, payload, requirements)
	switch check.verdict {
	case noVerdict:
		return nil, &NoVerdictError{Payer: check.payer, Err: check.cause}
	case paymentRefused:
		return nil, x402.NewSettleError(check.reason, check.payer, x402.Network(f.network()), "", causeText(check.cause))
	}
	// A request that ended while the node was read has had its answer, or lost
	// its caller: broadcasting now would move funds behind a response with no
	// verdict.
	if err := ctx.Err(); err != nil {
		return nil, &NoVerdictError{Payer: check.payer, Err: err}
	}
	hash, err := f.node.Broadcast(check.raw)
	if err != nil {
		// A broadcast that never reached the chain settles nothing and refuses
		// nothing. Upstream errors this call both for a transaction CheckTx
		// rejected and for one it timed out waiting to commit — and on the
		// timeout the transaction is already in the mempool and will commit. Only
		// the chain's own abci.Error separates the two.
		if !chainRefused(err) {
			return nil, &NoVerdictError{Payer: check.payer, Err: err}
		}
		return nil, x402.NewSettleError(ReasonBroadcastFailed, check.payer, x402.Network(f.network()), hash, err.Error())
	}
	return &x402.SettleResponse{Success: true, Transaction: hash, Network: x402.Network(f.network()), Payer: check.payer}, nil
}

func causeText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// chainIDReference is CAIP-2's grammar for the reference half of a network.
var chainIDReference = regexp.MustCompile(`^[-_a-zA-Z0-9]{1,32}$`)

// ValidChainID reports whether a chain id can be published as the reference half
// of this facilitator's network name, "gno:<chain-id>".
//
// x402 names networks in CAIP-2, which limits the reference to
// [-_a-zA-Z0-9]{1,32}, while tm2 accepts any non-empty chain id up to 50 bytes. A
// colon makes the network read as three parts, which upstream's parser and the JS
// buyer refuse, so every payment would fail without naming the configuration.
// Any other chain id outside the grammar, such as one with a dot, both accept
// today, but it names no CAIP-2 network and another implementation may refuse
// it. It is checked where a chain id enters the process rather than per request,
// because it cannot change while the process runs.
func ValidChainID(chainID string) error {
	if !chainIDReference.MatchString(chainID) {
		return fmt.Errorf("chain id %q cannot name a CAIP-2 network: want 1 to 32 of [-_a-zA-Z0-9]", chainID)
	}
	return nil
}

// paymentVerdict is what a check concluded about a payment.
type paymentVerdict int

const (
	// paymentAccepted reports a payment nothing the check read refuses.
	paymentAccepted paymentVerdict = iota

	// paymentRefused reports a payment that will not settle, with a reason
	// naming why.
	paymentRefused

	// noVerdict reports that the check could not run, because the chain it
	// decides against could not be read. It is not a refusal: the payment is
	// neither valid nor invalid here, and saying either would state something
	// the checker does not know.
	noVerdict
)

// paymentCheck is what a facilitator concluded about one payment: the verdict,
// the transaction's bytes when the payment is one to settle, the payer, the wire
// reason when it is refused, and the error behind a reason that covers several
// conditions or behind a failed read.
type paymentCheck struct {
	verdict paymentVerdict
	raw     []byte
	payer   string
	reason  string
	cause   error
}

// refusePayment reports a payment this facilitator will not settle.
func refusePayment(payer, reason string, cause error) paymentCheck {
	return paymentCheck{verdict: paymentRefused, payer: payer, reason: reason, cause: cause}
}

// check runs the full verification (network match, static rules, the signer's
// account, simulate) and reports what it concluded. A refusal names the payer
// only once the signature verified: until then the sender is whatever the
// transaction's bytes claim.
//
// The engine dispatches by scheme and by registered network, so neither needs
// rechecking here, except that a registration under a wildcard pattern hands this
// chain's mechanism a payment for another gno chain.
func (f *Facilitator) check(ctx context.Context, payload types.PaymentPayload, req types.PaymentRequirements) paymentCheck {
	if req.Network != f.network() {
		return refusePayment("", ReasonInvalidNetwork, nil)
	}
	if err := unsupportedTerms(req.Extra); err != nil {
		return refusePayment("", ReasonInvalidRequirements, err)
	}
	if !acceptsSameOffer(payload.Accepted, req) {
		return refusePayment("", ReasonInvalidPayload, nil)
	}
	if err := unsupportedTerms(payload.Accepted.Extra); err != nil {
		return refusePayment("", ReasonInvalidPayload, err)
	}
	// VerifyStatic refuses an unusable memo too, but discards why. A non-string
	// memo and an over-cap one share one wire reason, so the cause is read here
	// to keep the rejection diagnosable.
	if _, err := memo(req.Extra); err != nil {
		return refusePayment("", ReasonInvalidRequirements, err)
	}
	transaction, err := transactionOf(payload.Payload)
	if err != nil {
		return refusePayment("", ReasonMalformedTransaction, err)
	}
	raw, err := base64.StdEncoding.DecodeString(transaction)
	if err != nil {
		return refusePayment("", ReasonMalformedTransaction, err)
	}
	tx, payer, reason := verifyStatic(req, raw)
	if reason != "" {
		return refusePayment("", reason, nil)
	}
	// The signer's account decides the signature and the sequence, and it decides
	// them before anything is simulated. Simulation cannot: gno's auth ante skips
	// signature verification under simulate, so a forged signature and a
	// superseded one both pass it and fail at settle. Answering off the account
	// read alone also keeps a payment that cannot settle from costing the node a
	// simulation, on an endpoint that asks the caller for no credential.
	switch verdict, reason, cause := verifyAccountChecks(ctx, tx, f.node, f.chainID); verdict {
	case noVerdict:
		return paymentCheck{verdict: noVerdict, payer: payer, cause: cause}
	case paymentRefused:
		// A signature over the consumed sequence verified, so the address it names
		// is the payer's; every other account refusal verified no signature.
		if reason == ReasonSequenceMismatch {
			return refusePayment(payer, reason, cause)
		}
		return refusePayment("", reason, cause)
	}
	if err := f.node.Simulate(tx); err != nil {
		// A simulation that never reached the node refuses nothing, so it is not
		// answered as a refused payment either.
		if !chainRefused(err) {
			return paymentCheck{verdict: noVerdict, payer: payer, cause: err}
		}
		return refusePayment(payer, ReasonSimulationFailed, err)
	}
	return paymentCheck{verdict: paymentAccepted, raw: raw, payer: payer}
}

// unsupportedTerms refuses an extra naming a transfer method or a payment flow
// this mechanism does not implement. §6.1 reserves both keys, so a payment
// offered under either is not one this mechanism may settle.
func unsupportedTerms(extra map[string]any) error {
	if v, ok := extra["assetTransferMethod"]; ok && v != AssetTransferMethod {
		return fmt.Errorf("x402: extra.assetTransferMethod is %v, want %q", v, AssetTransferMethod)
	}
	if v, ok := extra["paymentFlow"]; ok && v != string(x402.PaymentFlowAuthorization) {
		return fmt.Errorf("x402: extra.paymentFlow is %v, want %q", v, x402.PaymentFlowAuthorization)
	}
	return nil
}

// transactionOf reads the scheme payload's transaction, the base64 of the signed
// std.Tx. An absent one reads as empty, which no transaction decodes from.
func transactionOf(payload map[string]any) (string, error) {
	raw, ok := payload["transaction"]
	if !ok {
		return "", nil
	}
	transaction, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("x402: payload transaction is %T, want string", raw)
	}
	return transaction, nil
}

// memo returns extra.memo, the memo the requirements bind the payment's
// transaction to, and "" when they bind none. A memo that is present but not a
// string, or longer than MaxMemoBytes, is an error rather than an absent memo:
// reading either as "" would silently drop the binding and yield a payment the
// seller rejects. This is the single enforcement point for the cap.
func memo(extra map[string]any) (string, error) {
	v, ok := extra["memo"]
	if !ok {
		return "", nil
	}
	m, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("x402: extra.memo is %T, want string", v)
	}
	if len(m) > MaxMemoBytes {
		return "", fmt.Errorf("x402: extra.memo is %d bytes, exceeding the %d-byte maximum", len(m), MaxMemoBytes)
	}
	return m, nil
}

// verifyAccountChecks reports what the signer's on-chain account says about this
// payment. Simulation cannot answer it: gno's auth ante skips signature
// verification under simulate, so a forged signature and a superseded sequence
// both pass it and fail only at settle.
//
// An account read that failed reports no verdict rather than a refusal: nothing
// verified the signature, and answering "invalid" would blame the payment for the
// reader's own outage. An account the chain says does not exist is a refusal —
// the ante resolves the signer's account before it verifies anything, so such a
// transaction is one neither simulation nor delivery would accept, and it reports
// the code simulating would have reported. That code, and the signer-count one,
// cover more than one condition, so the cause is returned alongside: it is what
// an operator has to tell them apart. A refused signature carries none — the
// reason is the whole diagnosis.
func verifyAccountChecks(ctx context.Context, tx *std.Tx, reader AccountReader, chainID string) (verdict paymentVerdict, reason string, cause error) {
	signer, err := txSigner(tx)
	if err != nil {
		return paymentRefused, ReasonSignatureCount, err
	}
	acc, err := reader.SignerAccount(ctx, tx)
	if err != nil {
		if !chainRefused(err) {
			return noVerdict, "", err
		}
		return paymentRefused, ReasonSimulationFailed, err
	}
	switch verifySignature(tx, acc, signer, chainID) {
	case signatureFresh:
		return paymentAccepted, "", nil
	case signatureConsumed:
		return paymentRefused, ReasonSequenceMismatch, nil
	default:
		return paymentRefused, ReasonSignatureInvalid, nil
	}
}

// chainRefused reports whether a chain access error is the chain's own answer
// about the transaction rather than a failure to reach it.
//
// gno answers every refusal it decides — a signer with no account, a balance too
// low, an expired session, a delivery the VM aborted — with an abci.Error, the
// type the node puts in the response it returns. A dial failure, a timeout, an
// RPC-level error and an encoding change carry none, and none of them says
// anything about the payment. The test is by type on purpose: tm2's JSON-RPC
// layer flattens its own failures into one code whose prose is the only
// difference, and no answer here may rest on an upstream error string.
func chainRefused(err error) bool {
	_, ok := errors.AsType[abci.Error](err)
	return ok
}

// acceptsSameOffer reports whether the payload's accepted object names the same
// offer as the requirements it answers. maxTimeoutSeconds is left out because it
// is advisory, and extra because either side may legitimately carry keys the
// other does not know; the five fields that identify the offer are all compared,
// which is what upstream's own matchers do.
//
// The scheme and the network are among them even though the requirements' copies
// were already dispatched on, because that says nothing about the payload's. A
// payload agreeing about the price while naming another scheme or another chain
// describes a different offer, and the mismatch has to be reported rather than
// read past.
//
// The signed transaction pins recipient, amount and chain-id, so a disagreement
// here cannot redirect funds — it means the client and the resource server
// understood the offer differently, which must be reported rather than silently
// resolved in the server's favor.
func acceptsSameOffer(accepted, required types.PaymentRequirements) bool {
	return accepted.Scheme == required.Scheme &&
		accepted.Network == required.Network &&
		accepted.PayTo == required.PayTo &&
		accepted.Amount == required.Amount &&
		accepted.Asset == required.Asset
}
