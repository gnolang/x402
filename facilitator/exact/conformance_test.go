package exact_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gnolang/x402/facilitator/exact"
)

// The reason codes §9 of the specification enumerates, transcribed verbatim from
// specs/x402-specification-v2.md at x402-foundation/x402 commit
// f8f83309706b14cc696962f0a002eeeb66c3dc26 (lines 593-608).
//
// Only the scheme-independent names are transcribed. §9 also lists EVM's
// invalid_exact_evm_payload_* codes, which belong to that scheme's vocabulary
// and say nothing about this one.
var specEnvelopeReasons = []string{
	"insufficient_funds",
	"invalid_network",
	"invalid_payload",
	"invalid_payment_requirements",
	"invalid_scheme",
	"unsupported_scheme",
	"invalid_x402_version",
	"invalid_transaction_state",
	"unexpected_verify_error",
	"unexpected_settle_error",
	"settlement_pending",
}

// TestEnvelopeReasonsAreSpecNames checks the envelope-level codes against the
// specification's list rather than against our own constants. A code that
// describes the envelope is a name every scheme shares, so inventing one — or
// misspelling one — publishes a token no client can interpret.
//
// Scheme-level codes are deliberately absent: gno's have no entry to match,
// which is what the invalid_exact_gno_ prefix declares.
func TestEnvelopeReasonsAreSpecNames(t *testing.T) {
	for _, reason := range envelopeLevelReasons {
		assert.Contains(t, specEnvelopeReasons, reason,
			"envelope-level reason %q is not a name the specification defines", reason)
	}
}

func TestMemo(t *testing.T) {
	atCap := strings.Repeat("a", exact.MaxMemoBytes)
	cases := []struct {
		name    string
		extra   map[string]any
		want    string
		wantErr bool
	}{
		{name: "no extra"},
		{name: "empty extra", extra: map[string]any{}},
		{name: "no memo key", extra: map[string]any{"name": "USDC"}},
		{name: "memo present", extra: map[string]any{"memo": "pay_1"}, want: "pay_1"},
		{name: "memo at the cap", extra: map[string]any{"memo": atCap}, want: atCap},
		{name: "memo over the cap", extra: map[string]any{"memo": atCap + "a"}, wantErr: true},
		// A non-string memo must be a hard error, never a silent "": treating it
		// as absent would drop the memo requirement entirely and hand back a
		// payment the seller then rejects.
		{name: "memo is a number", extra: map[string]any{"memo": 123}, wantErr: true},
		{name: "memo is null", extra: map[string]any{"memo": nil}, wantErr: true},
		{name: "memo is an object", extra: map[string]any{"memo": map[string]any{"a": "b"}}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := exact.Memo(tc.extra)
			if tc.wantErr {
				require.Error(t, err)
				assert.Empty(t, got, "a rejected memo must not also be returned")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
