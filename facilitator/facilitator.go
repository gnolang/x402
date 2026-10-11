// Package facilitator serves the x402 facilitator API — /verify, /settle and
// /supported — for one gno chain (spec: x402 v2).
//
// Payments are decided by upstream's facilitator engine with gno's exact
// mechanism (package exact) registered on it. This package is the HTTP transport
// around them: it throttles the endpoints that reach the chain, caps and parses
// request bodies, reports a refusal in the specification's §9 vocabulary, and
// answers 503 with no verdict when the chain could not be read.
package facilitator

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	x402 "github.com/x402-foundation/x402/go/v2"

	"github.com/gnoverse/x402/facilitator/exact"
)

// engine is upstream's facilitator engine, as the transport drives it.
type engine interface {
	Verify(ctx context.Context, payload, requirements []byte) (*x402.VerifyResponse, error)
	Settle(ctx context.Context, payload, requirements []byte) (*x402.SettleResponse, error)
	GetSupported() x402.SupportedResponse
}

// Server serves the x402 facilitator API for one gno chain.
type Server struct {
	engine  engine
	network string
	limiter *rateLimiter
}

// Option adjusts a Server at construction.
type Option func(*Server)

// WithRateLimit replaces the default per-remote-address throttle on /verify and
// /settle. Zero fields keep their defaults.
func WithRateLimit(rl RateLimit) Option {
	return func(s *Server) { s.limiter = newRateLimiter(rl) }
}

// New serves the chain whose id is chainID — the CAIP-2 reference of network
// "gno:<chainID>" — with gno's exact mechanism registered on upstream's engine.
// The chain-touching endpoints are throttled at the default rate unless
// WithRateLimit says otherwise.
//
// The chain id is expected to satisfy exact.ValidChainID; the command checks it
// as it parses the flag, before anything is served.
func New(node exact.Node, chainID string, opts ...Option) *Server {
	network := "gno:" + chainID
	s := &Server{
		engine: x402.Newx402Facilitator().Register(
			[]x402.Network{x402.Network(network)}, exact.NewFacilitator(node, chainID)),
		network: network,
		limiter: newRateLimiter(RateLimit{}),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Handler returns the facilitator HTTP routes. Both endpoints that reach the
// chain are throttled; /supported answers from the registration and is not.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST /verify", s.throttled(s.handleVerify))
	mux.Handle("POST /settle", s.throttled(s.handleSettle))
	mux.HandleFunc("GET /supported", s.handleSupported)
	return mux
}

// throttled fronts a chain-touching handler with the per-remote-address token
// bucket. Both endpoints answer anyone, and a payment that decodes costs a chain
// read before it can be refused — the signer's account, plus a simulation on top
// once the signature checks out — so an unthrottled endpoint lets a caller with
// no key at all spend the node's capacity at will.
func (s *Server) throttled(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.limiter.allow(peerAddress(r)) {
			rejectRateLimited(w)
			return
		}
		next(w, r)
	})
}

// rejectRateLimited answers a caller that asked too often. The answer is fixed,
// and the record names no address: the key is whatever the connection reported,
// and this package logs no peer addresses.
func rejectRateLimited(w http.ResponseWriter) {
	slog.Info("x402: reject request", "detail", "rate_limited")
	http.Error(w, "too many requests", http.StatusTooManyRequests)
}

func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeRequest(w, r)
	if !ok {
		return
	}
	if req.payloadVersion != x402.ProtocolVersion {
		refuseVerify(w, "", exact.ReasonInvalidVersion, "")
		return
	}
	res, err := s.engine.Verify(r.Context(), req.PaymentPayload, req.PaymentRequirements)
	if err != nil {
		s.answerVerifyError(w, req, err)
		return
	}
	slog.Info("x402 verify: payment valid", "payer", res.Payer)
	writeJSON(w, res)
}

func (s *Server) answerVerifyError(w http.ResponseWriter, req request, err error) {
	if undecided, ok := errors.AsType[*exact.NoVerdictError](err); ok {
		rejectNoVerdict(w, "x402 verify", undecided.Payer, undecided.Err)
		return
	}
	refusal, ok := errors.AsType[*x402.VerifyError](err)
	if !ok {
		// The engine and the mechanism report nothing else, and an error of
		// another kind establishes nothing about the payment.
		rejectNoVerdict(w, "x402 verify", "", err)
		return
	}
	refuseVerify(w, refusal.Payer, s.specReason(refusal.InvalidReason, req.requirementsScheme), refusal.InvalidMessage)
}

// refuseVerify answers a payment that will not settle. The payer is logged and
// left out of the answer.
func refuseVerify(w http.ResponseWriter, payer, reason, cause string) {
	slog.Info("x402 verify: payment invalid", rejectionFields(payer, reason, cause)...)
	writeJSON(w, x402.VerifyResponse{IsValid: false, InvalidReason: reason})
}

func (s *Server) handleSettle(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeRequest(w, r)
	if !ok {
		return
	}
	if req.payloadVersion != x402.ProtocolVersion {
		s.refuseSettle(w, "", exact.ReasonInvalidVersion, "")
		return
	}
	res, err := s.engine.Settle(r.Context(), req.PaymentPayload, req.PaymentRequirements)
	if err != nil {
		s.answerSettleError(w, req, err)
		return
	}
	slog.Info("x402 settle: payment settled", "payer", res.Payer, "tx", res.Transaction)
	writeJSON(w, res)
}

func (s *Server) answerSettleError(w http.ResponseWriter, req request, err error) {
	if undecided, ok := errors.AsType[*exact.NoVerdictError](err); ok {
		rejectNoVerdict(w, "x402 settle", undecided.Payer, undecided.Err)
		return
	}
	refusal, ok := errors.AsType[*x402.SettleError](err)
	if !ok {
		rejectNoVerdict(w, "x402 settle", "", err)
		return
	}
	reason := s.specReason(refusal.ErrorReason, req.requirementsScheme)
	if reason == exact.ReasonBroadcastFailed {
		// A delivery the chain committed and then aborted charged the payer its
		// fee, so its hash is a real record even though the answer carries an
		// empty transaction. It reaches the operator's log, which is where the
		// charge can be reconciled.
		slog.Error("x402 settle: broadcast failed", "payer", refusal.Payer, "tx", refusal.Transaction, "err", refusal.ErrorMessage)
		writeJSON(w, x402.SettleResponse{Success: false, Network: x402.Network(s.network), Payer: refusal.Payer, ErrorReason: reason})
		return
	}
	s.refuseSettle(w, refusal.Payer, reason, refusal.ErrorMessage)
}

// refuseSettle answers a payment that will not settle. The payer is reported
// whenever verification decoded one: it is omitempty, so a refusal before the
// transaction decoded still names nobody, and the spec's own failure fixture
// carries the key.
func (s *Server) refuseSettle(w http.ResponseWriter, payer, reason, cause string) {
	slog.Info("x402 settle: payment invalid", rejectionFields(payer, reason, cause)...)
	writeJSON(w, x402.SettleResponse{Success: false, Network: x402.Network(s.network), Payer: payer, ErrorReason: reason})
}

// specReason names an engine refusal in the specification's §9 vocabulary. The
// engine reports its own routing failures under codes of its own; the
// mechanism's codes already are spec or scheme names and pass through.
func (s *Server) specReason(code, scheme string) string {
	switch code {
	case x402.ErrInvalidVersion:
		return exact.ReasonInvalidVersion
	case x402.ErrInvalidV2Payload:
		return exact.ReasonInvalidPayload
	case x402.ErrInvalidV2Requirements:
		return exact.ReasonInvalidRequirements
	case x402.ErrNoFacilitatorForNetwork:
		if !s.servesScheme(scheme) {
			return exact.ReasonUnsupportedScheme
		}
		return exact.ReasonInvalidNetwork
	}
	return code
}

func (s *Server) servesScheme(scheme string) bool {
	for _, kind := range s.engine.GetSupported().Kinds {
		if kind.Scheme == scheme {
			return true
		}
	}
	return false
}

// rejectionFields describes a rejected payment: the payer when verification
// decoded one, the reason code, and the chain error when one caused it.
func rejectionFields(payer, reason, cause string) []any {
	var fields []any
	if payer != "" {
		fields = append(fields, "payer", payer)
	}
	fields = append(fields, "reason", reason)
	if cause != "" {
		fields = append(fields, "err", cause)
	}
	return fields
}

// rejectNoVerdict answers a caller the facilitator cannot decide for: its own
// chain access failed, so it holds nothing to report about the payment.
//
// A 200 carrying isValid:false would state a verdict instead — one that looks
// permanent, over every payment that arrives for as long as the node is
// unreachable — so a client believing it discards payments that may be
// perfectly good. No scheme reason code is minted for the condition either: a
// facilitator's own downtime does not belong in a published wire vocabulary.
func rejectNoVerdict(w http.ResponseWriter, phase, payer string, cause error) {
	var fields []any
	if payer != "" {
		fields = append(fields, "payer", payer)
	}
	fields = append(fields, "err", cause)
	slog.Error(phase+": chain unreachable, reporting no verdict", fields...)
	http.Error(w, "the payment could not be checked against the chain", http.StatusServiceUnavailable)
}

func (s *Server) handleSupported(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.engine.GetSupported())
}

// request is the body of POST /verify and POST /settle. The payload and the
// requirements stay as received, for the engine to parse. A request that omits
// the top-level x402Version asserts no version; the payload's own version is the
// one enforced.
type request struct {
	X402Version         int             `json:"x402Version,omitempty"`
	PaymentPayload      json.RawMessage `json:"paymentPayload"`
	PaymentRequirements json.RawMessage `json:"paymentRequirements"`

	// payloadVersion is the version the payload declares: the engine routes
	// version 1 to a handler this facilitator does not register, so the
	// transport refuses it in the specification's own words first.
	payloadVersion int

	// requirementsScheme tells an unserved scheme from an unserved network, which
	// the engine reports under one code.
	requirementsScheme string
}

// decodeRequest reads the request body both endpoints take, capped at 1 MiB.
func decodeRequest(w http.ResponseWriter, r *http.Request) (request, bool) {
	var req request
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := decoder.Decode(&req); err != nil {
		rejectRequestBody(w, "malformed_body")
		return request{}, false
	}
	// Decode reads the first JSON value and stops, so trailing content would be
	// dropped in silence: two readers of one body could then disagree about which
	// payment was requested. Unknown fields *inside* the object stay accepted —
	// the protocol requires a facilitator to tolerate what it does not know.
	if err := decoder.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		rejectRequestBody(w, "trailing_content")
		return request{}, false
	}

	var payload struct {
		X402Version int `json:"x402Version"`
	}
	var requirements struct {
		Scheme string `json:"scheme"`
	}
	if !decodePresent(req.PaymentPayload, &payload) || !decodePresent(req.PaymentRequirements, &requirements) {
		rejectRequestBody(w, "malformed_body")
		return request{}, false
	}
	req.payloadVersion = payload.X402Version
	req.requirementsScheme = requirements.Scheme
	return req, true
}

// decodePresent decodes raw into v unless the field was absent, which leaves v
// zero.
func decodePresent(raw json.RawMessage, v any) bool {
	return len(raw) == 0 || json.Unmarshal(raw, v) == nil
}

// rejectRequestBody refuses a body the facilitator could not read as exactly
// one request. The answer is fixed: the decoder's error text quotes the caller's
// own bytes and the parser's internals, and this endpoint answers anyone. The
// detail names which check refused, and reaches the log only.
func rejectRequestBody(w http.ResponseWriter, detail string) {
	slog.Info("x402: reject request body", "detail", detail)
	http.Error(w, "invalid request body", http.StatusBadRequest)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("x402: write response", "err", err)
	}
}
