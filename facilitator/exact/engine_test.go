package exact_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	x402 "github.com/x402-foundation/x402/go/v2"
	"github.com/x402-foundation/x402/go/v2/types"

	"github.com/gnolang/x402/facilitator/exact"
	"github.com/gnolang/x402/facilitator/exact/exacttest"
)

// engineFor registers the gno mechanism on upstream's facilitator engine the way
// any Go x402 facilitator adds a network: one Register call, nothing gno-specific
// beyond the mechanism itself.
func engineFor(node exact.Node) interface {
	Verify(ctx context.Context, payload, requirements []byte) (*x402.VerifyResponse, error)
	Settle(ctx context.Context, payload, requirements []byte) (*x402.SettleResponse, error)
	GetSupported() x402.SupportedResponse
} {
	return x402.Newx402Facilitator().Register(
		[]x402.Network{"gno:" + exacttest.ChainID},
		exact.NewFacilitator(node, exacttest.ChainID))
}

func paymentBytes(t *testing.T, requirements types.PaymentRequirements, txB64 string) (payload, req []byte) {
	t.Helper()
	payload, err := json.Marshal(exacttest.PaymentPayload(requirements, txB64))
	require.NoError(t, err)
	req, err = json.Marshal(requirements)
	require.NoError(t, err)
	return payload, req
}

// TestTheMechanismVerifiesAndSettlesThroughUpstreamsEngine is the claim the
// mechanism exists to make: registered on upstream's own engine, it verifies and
// settles a payment signed for real, and the engine publishes it under gno's
// CAIP-2 family.
func TestTheMechanismVerifiesAndSettlesThroughUpstreamsEngine(t *testing.T) {
	txB64, account := exacttest.SignedTx(t, nil)
	node := &exacttest.Node{Account: account, Hash: "abc123"}
	engine := engineFor(node)
	payload, req := paymentBytes(t, exacttest.Requirements(), txB64)

	verified, err := engine.Verify(context.Background(), payload, req)
	require.NoError(t, err)
	assert.True(t, verified.IsValid)
	assert.Equal(t, exacttest.Payer, verified.Payer)
	assert.Zero(t, node.Broadcasts, "verify never writes to the chain")

	settled, err := engine.Settle(context.Background(), payload, req)
	require.NoError(t, err)
	assert.True(t, settled.Success)
	assert.Equal(t, "abc123", settled.Transaction)
	assert.Equal(t, x402.Network("gno:dev"), settled.Network)
	assert.Equal(t, exacttest.Payer, settled.Payer)
	assert.Equal(t, 1, node.Broadcasts)

	supported := engine.GetSupported()
	require.Len(t, supported.Kinds, 1)
	assert.Equal(t, 2, supported.Kinds[0].X402Version)
	assert.Equal(t, "exact", supported.Kinds[0].Scheme)
	assert.Equal(t, "gno:dev", supported.Kinds[0].Network)
	assert.Empty(t, supported.Signers["gno:*"], "the facilitator holds no key, so it publishes no signer")
}

// TestARefusalReachesTheEngineAsAVerifyError pins the upstream convention the
// transport relies on: a payment the mechanism refuses is an error carrying the
// reason code, not a response.
func TestARefusalReachesTheEngineAsAVerifyError(t *testing.T) {
	txB64, account := exacttest.SignedTx(t, nil)
	requirements := exacttest.Requirements()
	requirements.Amount = "1"
	payload, req := paymentBytes(t, requirements, txB64)

	_, err := engineFor(&exacttest.Node{Account: account}).Verify(context.Background(), payload, req)

	refusal, ok := errors.AsType[*x402.VerifyError](err)
	require.True(t, ok, "want *x402.VerifyError, got %v", err)
	assert.Equal(t, exact.ReasonAmountMismatch, refusal.InvalidReason)
}

// TestAnUnreadableChainReachesTheEngineAsNoVerdict pins the other outcome: a
// facilitator that could not read the chain has decided nothing, and says so
// with a type no reason code is attached to.
func TestAnUnreadableChainReachesTheEngineAsNoVerdict(t *testing.T) {
	txB64, account := exacttest.SignedTx(t, nil)
	node := &exacttest.Node{Account: account, AccountErr: errors.New("dial tcp: connection refused")}
	payload, req := paymentBytes(t, exacttest.Requirements(), txB64)

	_, err := engineFor(node).Settle(context.Background(), payload, req)

	undecided, ok := errors.AsType[*exact.NoVerdictError](err)
	require.True(t, ok, "want *exact.NoVerdictError, got %v", err)
	assert.Equal(t, exacttest.Payer, undecided.Payer)
	assert.ErrorContains(t, undecided, "connection refused")
	assert.Zero(t, node.Broadcasts)
}
