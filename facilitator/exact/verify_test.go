package exact_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/gnolang/gno/gno.land/pkg/sdk/vm"
	"github.com/gnolang/gno/tm2/pkg/crypto"
	"github.com/gnolang/gno/tm2/pkg/crypto/ed25519"
	"github.com/gnolang/gno/tm2/pkg/crypto/multisig"
	"github.com/gnolang/gno/tm2/pkg/sdk/bank"
	"github.com/gnolang/gno/tm2/pkg/std"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/x402-foundation/x402/go/v2/types"

	"github.com/gnoverse/x402/facilitator/exact"
	"github.com/gnoverse/x402/facilitator/exact/exacttest"
)

// The reason vocabulary, partitioned by level. The partition is what the tests
// below check each half against, so a new code joins one of these lists and is
// held to that level's rules.
var (
	schemeLevelReasons = []string{
		exact.ReasonMalformedTransaction,
		exact.ReasonUnexpectedMessage,
		exact.ReasonSignatureCount,
		exact.ReasonRecipientMismatch,
		exact.ReasonAmountMismatch,
		exact.ReasonMemoMismatch,
		exact.ReasonSimulationFailed,
		exact.ReasonBroadcastFailed,
		exact.ReasonSignatureInvalid,
		exact.ReasonSequenceMismatch,
	}
	envelopeLevelReasons = []string{
		exact.ReasonInvalidPayload,
		exact.ReasonInvalidVersion,
		exact.ReasonInvalidNetwork,
		exact.ReasonInvalidRequirements,
		exact.ReasonUnsupportedScheme,
		exact.ReasonUnexpectedSettleError,
	}
)

// TestReasonVocabulary pins the shape of the reason vocabulary. Scheme-level
// codes carry the invalid_exact_<caip2-namespace> prefix the peer schemes use —
// "gno" is the CAIP-2 namespace, matching how SVM's constants say "solana" —
// while envelope-level codes are the spec's own §9 names and carry no scheme
// prefix. Reason strings are the one part of the wire that cannot be changed
// once integrators depend on them, so a collision or a stray prefix must fail
// here rather than after publication.
func TestReasonVocabulary(t *testing.T) {
	for _, reason := range schemeLevelReasons {
		assert.True(t, strings.HasPrefix(reason, "invalid_exact_gno_"),
			"scheme-level reason %q must name the scheme and the CAIP-2 namespace", reason)
	}
	for _, reason := range envelopeLevelReasons {
		assert.False(t, strings.HasPrefix(reason, "invalid_exact_"),
			"envelope-level reason %q is not scheme-specific", reason)
	}

	seen := make(map[string]bool)
	for _, reason := range slices.Concat(schemeLevelReasons, envelopeLevelReasons) {
		require.NotEmpty(t, reason)
		assert.False(t, seen[reason], "two conditions must not report the same reason: %q", reason)
		seen[reason] = true
	}
}

// TestVerifyStatic_SignatureCount separates "wrong number of signatures" from
// "could not be decoded". A transaction bearing zero or two signatures decodes
// perfectly well; reporting it as undecodable told the payer to look in the
// wrong place.
func TestVerifyStatic_SignatureCount(t *testing.T) {
	cases := map[string]func(*std.Tx){
		"none": func(tx *std.Tx) { tx.Signatures = nil },
		"two": func(tx *std.Tx) {
			tx.Signatures = append(tx.Signatures, std.Signature{Signature: []byte("sig2")})
		},
		"three": func(tx *std.Tx) {
			tx.Signatures = append(tx.Signatures, std.Signature{Signature: []byte("sig2")}, std.Signature{Signature: []byte("sig3")})
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			tx, _, reason := exact.VerifyStatic(exacttest.Requirements(), exacttest.PlaceholderTx(t, mutate))
			assert.Equal(t, exact.ReasonSignatureCount, reason)
			assert.Nil(t, tx)
		})
	}
}

// TestVerifyStatic_SubKeyCount refuses a multisig key before its subkeys are
// verified. Capping the signature count at one bounds nothing on its own: one
// signature may carry a threshold key holding thousands of subkeys, and
// verifying it costs an ed25519 check per subkey. The chain counts subkeys in
// its very first ante check, before any account read, and this check exists to
// keep that ordering — the whole reason static verification runs ahead of
// simulation is to refuse cheaply.
func TestVerifyStatic_SubKeyCount(t *testing.T) {
	subKeys := make([]crypto.PubKey, 8)
	for i := range subKeys {
		subKeys[i] = ed25519.GenPrivKey().PubKey()
	}
	multi := multisig.NewPubKeyMultisigThreshold(1, subKeys)

	tx, _, reason := exact.VerifyStatic(exacttest.Requirements(), exacttest.PlaceholderTx(t, func(tx *std.Tx) {
		tx.Signatures[0].PubKey = multi
	}))
	assert.Equal(t, exact.ReasonSignatureCount, reason)
	assert.Nil(t, tx)
}

// TestVerifyStatic_ThresholdKeyBelowItsOwnSubKeyCount refuses a threshold key
// whose subkeys number one, which the subkey cap alone lets through:
// std.CountSubKeys sums its subkeys and answers 1, the same as an ordinary key.
//
// The count is not the only thing wrong with such a key. A threshold of zero is
// unconstructible through multisig.NewPubKeyMultisigThreshold, which panics on
// it, but amino decodes the struct whatever its fields say — and the verifier
// bounds its signature list against that threshold, so a zero one admits a
// signature list holding nothing while the bit array claims a signature is set.
// A key shape the chain's own constructor refuses has no business reaching
// verification, so the cap refuses the type outright.
func TestVerifyStatic_ThresholdKeyBelowItsOwnSubKeyCount(t *testing.T) {
	multi := multisig.PubKeyMultisigThreshold{K: 0, PubKeys: []crypto.PubKey{ed25519.GenPrivKey().PubKey()}}
	require.Equal(t, 1, std.CountSubKeys(multi), "the cap cannot rest on the subkey count for this shape")

	tx, _, reason := exact.VerifyStatic(exacttest.Requirements(), exacttest.PlaceholderTx(t, func(tx *std.Tx) {
		tx.Signatures[0].PubKey = multi
	}))
	assert.Equal(t, exact.ReasonSignatureCount, reason)
	assert.Nil(t, tx)
}

// TestVerifyStatic_SingleKeyAndOmittedKeyAccepted pins the two shapes the
// subkey cap must not refuse: an ordinary single key, and no key at all — a
// signature omits its key when the account already stores one, and
// std.CountSubKeys reports 1 for a nil key precisely so that case still works.
func TestVerifyStatic_SingleKeyAndOmittedKeyAccepted(t *testing.T) {
	cases := map[string]crypto.PubKey{
		"single key": ed25519.GenPrivKey().PubKey(),
		"no key":     nil,
	}
	for name, key := range cases {
		t.Run(name, func(t *testing.T) {
			tx, _, reason := exact.VerifyStatic(exacttest.Requirements(), exacttest.PlaceholderTx(t, func(tx *std.Tx) {
				tx.Signatures[0].PubKey = key
			}))
			assert.Empty(t, reason)
			assert.NotNil(t, tx)
		})
	}
}

// TestValidChainID pins the rule the network name rests on. A network is
// "gno:<chain-id>" built by concatenation, and CAIP-2 names exactly two
// colon-separated parts — so a chain id carrying one produces a network string
// that reads as three. Upstream's own parser refuses it and the JS buyer returns
// null for it, which means every payment for such a facilitator is refused with
// no indication that the configuration is what is wrong.
func TestValidChainID(t *testing.T) {
	valid := []string{"dev", "test14", "portal-loop", "staging.gno.land"}
	for _, chainID := range valid {
		t.Run(chainID, func(t *testing.T) {
			assert.NoError(t, exact.ValidChainID(chainID))
		})
	}

	invalid := map[string]string{
		"empty":            "",
		"a colon":          "test:14",
		"a leading colon":  ":dev",
		"a trailing colon": "dev:",
		"only a colon":     ":",
	}
	for name, chainID := range invalid {
		t.Run(name, func(t *testing.T) {
			assert.Error(t, exact.ValidChainID(chainID))
		})
	}
}

func TestVerifyStatic_ValidTx(t *testing.T) {
	tx, payer, reason := exact.VerifyStatic(exacttest.Requirements(), exacttest.PlaceholderTx(t, nil))
	assert.Empty(t, reason)
	require.NotNil(t, tx)
	assert.Equal(t, "g1jg8mtutu9khhfwc4nxmuhcpftf0pajdhfvsqf5", payer)
}

func TestVerifyStatic_Rejections(t *testing.T) {
	to := crypto.MustAddressFromString("g1jg8mtutu9khhfwc4nxmuhcpftf0pajdhfvsqf5") // wrong recipient

	cases := []struct {
		name   string
		req    types.PaymentRequirements
		txB64  string
		reason string
	}{
		{"garbage bytes", exacttest.Requirements(), "AAAA", exact.ReasonMalformedTransaction},
		{"invalid base64", exacttest.Requirements(), "%%%", exact.ReasonMalformedTransaction},
		{"wrong recipient", exacttest.Requirements(), exacttest.PlaceholderTx(t, func(tx *std.Tx) {
			msg := tx.Msgs[0].(bank.MsgSend)
			msg.ToAddress = to
			tx.Msgs[0] = msg
		}), exact.ReasonRecipientMismatch},
		{"wrong amount", exacttest.Requirements(), exacttest.PlaceholderTx(t, func(tx *std.Tx) {
			msg := tx.Msgs[0].(bank.MsgSend)
			msg.Amount = std.MustParseCoins("1ugnot")
			tx.Msgs[0] = msg
		}), exact.ReasonAmountMismatch},
		{"wrong denom", exacttest.Requirements(), exacttest.PlaceholderTx(t, func(tx *std.Tx) {
			msg := tx.Msgs[0].(bank.MsgSend)
			msg.Amount = std.MustParseCoins("250000uatom")
			tx.Msgs[0] = msg
		}), exact.ReasonAmountMismatch},
		{"two messages", exacttest.Requirements(), exacttest.PlaceholderTx(t, func(tx *std.Tx) {
			tx.Msgs = append(tx.Msgs, tx.Msgs[0])
		}), exact.ReasonUnexpectedMessage},
		{"non-MsgSend message", exacttest.Requirements(), exacttest.PlaceholderTx(t, func(tx *std.Tx) {
			tx.Msgs = []std.Msg{vm.MsgCall{
				Caller:  crypto.MustAddressFromString("g1jg8mtutu9khhfwc4nxmuhcpftf0pajdhfvsqf5"),
				PkgPath: "gno.land/r/demo/foo",
				Func:    "Bar",
			}}
		}), exact.ReasonUnexpectedMessage},
		{"memo required but missing", func() types.PaymentRequirements {
			r := exacttest.Requirements()
			r.Extra = map[string]any{"memo": "pay_1"}
			return r
		}(), exacttest.PlaceholderTx(t, nil), exact.ReasonMemoMismatch},
		// A hostile requirements object must not verify as memo-less: an
		// unusable memo requirement is a broken requirements object, and
		// reporting it as satisfied would settle an unbound payment.
		{"memo present but not a string", func() types.PaymentRequirements {
			r := exacttest.Requirements()
			r.Extra = map[string]any{"memo": 123}
			return r
		}(), exacttest.PlaceholderTx(t, nil), exact.ReasonInvalidRequirements},
		{"memo over the cap", func() types.PaymentRequirements {
			r := exacttest.Requirements()
			r.Extra = map[string]any{"memo": strings.Repeat("a", exact.MaxMemoBytes+1)}
			return r
		}(), exacttest.PlaceholderTx(t, func(tx *std.Tx) { tx.Memo = strings.Repeat("a", exact.MaxMemoBytes+1) }), exact.ReasonInvalidRequirements},
		{"no signature", exacttest.Requirements(), exacttest.PlaceholderTx(t, func(tx *std.Tx) {
			tx.Signatures = nil
		}), exact.ReasonSignatureCount},
		{"bad amount in requirements", func() types.PaymentRequirements {
			r := exacttest.Requirements()
			r.Amount = "not-a-number"
			return r
		}(), exacttest.PlaceholderTx(t, nil), exact.ReasonAmountMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tx, _, reason := exact.VerifyStatic(tc.req, tc.txB64)
			assert.Equal(t, tc.reason, reason)
			assert.Nil(t, tx)
		})
	}
}

// TestVerifyStatic_MemoBindingHoldsAcrossVerifications pins, without an HTTP
// facilitator or a node, what the memo-binding integration test proves over the
// wire: one memo-bound requirements value accepts the transaction carrying that
// memo and rejects a memo-less one. The steps share the requirements and run in
// order, so an accepted verification that left the memo requirement behind would
// surface as the second step passing.
func TestVerifyStatic_MemoBindingHoldsAcrossVerifications(t *testing.T) {
	const memo = "pay_bound"
	req := exacttest.Requirements()
	req.Extra = map[string]any{"memo": memo}

	steps := []struct {
		name   string
		txB64  string
		reason string
	}{
		{"bound memo accepted", exacttest.PlaceholderTx(t, func(tx *std.Tx) { tx.Memo = memo }), ""},
		{"memo-less rejected", exacttest.PlaceholderTx(t, nil), exact.ReasonMemoMismatch},
	}
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			tx, _, reason := exact.VerifyStatic(req, st.txB64)
			require.Equal(t, st.reason, reason)
			if st.reason == "" {
				require.NotNil(t, tx)
				return
			}
			require.Nil(t, tx)
		})
	}
}

func TestVerifyStatic_MemoMatchAccepted(t *testing.T) {
	req := exacttest.Requirements()
	req.Extra = map[string]any{"memo": "pay_1"}
	tx, _, reason := exact.VerifyStatic(req, exacttest.PlaceholderTx(t, func(tx *std.Tx) { tx.Memo = "pay_1" }))
	assert.Empty(t, reason)
	assert.NotNil(t, tx)
}
