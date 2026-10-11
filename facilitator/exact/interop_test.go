package exact_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gnolang/gno/tm2/pkg/sdk/bank"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/x402-foundation/x402/go/v2/types"

	"github.com/gnoverse/x402/facilitator/exact"
	"github.com/gnoverse/x402/facilitator/exact/exacttest"
)

// The interop fixture is a payment signed by @gnolang/gno-js-client, the
// official JS SDK, standing in for an x402 client that has never heard of gno.
// Its bytes are what such a client would put in the payload's transaction
// field: base64 of the SDK's own Tx encoding.
//
// The fixture is committed, so this test needs no JS toolchain. Regenerating it
// needs npm and is only for bumping the SDK or changing what the buyer signs.
//
// The expected payer is the well-known test1 address, derived from the mnemonic
// by the JS SDK and asserted against the value the Go side already trusts — so a
// divergence in key derivation or bech32 encoding fails rather than passes.
//
//go:generate ./testdata/gnojs/regen.sh
const (
	interopFixture = "gnojs_signed_send.b64"
	interopPayer   = "g1jg8mtutu9khhfwc4nxmuhcpftf0pajdhfvsqf5"
	interopPayTo   = "g1u7y667z64x2h7vc6fmpcprgey4ck233jaww9zq"
	interopAmount  = "250000"
	interopMemo    = "x402-interop"
)

func interopRequirements() types.PaymentRequirements {
	return types.PaymentRequirements{
		Scheme:  "exact",
		Network: "gno:dev",
		Amount:  interopAmount,
		Asset:   "ugnot",
		PayTo:   interopPayTo,
		Extra:   map[string]any{"memo": interopMemo},
	}
}

func interopTransaction(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", interopFixture))
	require.NoError(t, err, "interop fixture missing — regenerate with go generate ./facilitator/exact")
	return strings.TrimSpace(string(raw))
}

// TestVerifyStaticAcceptsJSSignedPayment is the cross-SDK wire check: a payment
// this package never encoded, produced by the JS SDK a foreign client would
// use, must decode and satisfy every static rule. It covers the tx encoding, the
// message's registered type name, address derivation and coin representation —
// the whole surface a foreign buyer touches, and the one thing no amount of
// Go-side fixture testing can prove.
//
// The sample is frozen, so what this detects is the Go side moving away from
// bytes a JS buyer once produced. It cannot notice the JS SDK moving: that
// direction is caught by make test-e2e, which signs with the installed client
// against a real node.
func TestVerifyStaticAcceptsJSSignedPayment(t *testing.T) {
	tx, payer, reason := exact.VerifyStatic(interopRequirements(), interopTransaction(t))

	require.Empty(t, reason, "JS-signed payment refused")
	require.NotNil(t, tx)
	assert.Equal(t, interopPayer, payer, "payer address disagrees across SDKs")

	require.Len(t, tx.Msgs, 1)
	send, ok := tx.Msgs[0].(bank.MsgSend)
	require.True(t, ok, "decoded message is %T, want bank.MsgSend", tx.Msgs[0])
	assert.Equal(t, interopPayer, send.FromAddress.String())
	assert.Equal(t, interopPayTo, send.ToAddress.String())
	assert.Equal(t, interopAmount+"ugnot", send.Amount.String())
	assert.Equal(t, interopMemo, tx.Memo, "memo binding lost in the JS encoding")
	require.Len(t, tx.Signatures, 1)
	assert.NotEmpty(t, tx.Signatures[0].Signature, "signature absent")
	assert.NotNil(t, tx.Signatures[0].PubKey, "public key absent, so no account can verify the signature")
}

// TestVerifyAccountChecksAcceptsJSSignedPayment checks the JS SDK's signature
// against the account it was signed for (number 0, sequence 0, no stored key).
// The SDK signs the fee as gas_wanted/gas_fee, a rendering the chain still
// accepts, so refusing it would refuse every payment such a buyer makes.
func TestVerifyAccountChecksAcceptsJSSignedPayment(t *testing.T) {
	tx, _, reason := exact.VerifyStatic(interopRequirements(), interopTransaction(t))
	require.Empty(t, reason)

	t.Run("at the signed sequence", func(t *testing.T) {
		assert.Empty(t, accountReason(t, tx, &exacttest.Node{Account: exacttest.AccountWithKey(nil, 0, 0)}))
	})

	t.Run("one sequence later is reported stale", func(t *testing.T) {
		assert.Equal(t, exact.ReasonSequenceMismatch, accountReason(t, tx, &exacttest.Node{Account: exacttest.AccountWithKey(nil, 0, 1)}))
	})
}
