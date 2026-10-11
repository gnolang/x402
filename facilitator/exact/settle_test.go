package exact_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gnolang/gno/tm2/pkg/std"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	x402 "github.com/x402-foundation/x402/go/v2"

	"github.com/gnoverse/x402/facilitator/exact"
	"github.com/gnoverse/x402/facilitator/exact/exacttest"
)

func settle(f *exact.Facilitator, txB64 string) (*x402.SettleResponse, error) {
	return f.Settle(context.Background(),
		exacttest.PaymentPayload(exacttest.Requirements(), txB64), exacttest.Requirements(), nil)
}

func requireDuplicate(t *testing.T, err error) {
	t.Helper()
	refusal, ok := errors.AsType[*x402.SettleError](err)
	require.True(t, ok, "want *x402.SettleError, got %v", err)
	assert.Equal(t, exact.ReasonDuplicateSettlement, refusal.ErrorReason)
	assert.Empty(t, refusal.Payer, "a copy refused before any check verified no signature")
	assert.Empty(t, refusal.Transaction, "a copy refused before any check broadcast nothing")
}

// TestSettle_RefusesACopyOfAPaymentBeingSettled holds one settle inside its
// broadcast and settles a copy of the same payment meanwhile. Both would pass
// every check, since the chain has not consumed the sequence yet, and two nodes
// would each admit one and answer both with the one delivery: one payment, two
// resources. The copy is refused without touching the node.
func TestSettle_RefusesACopyOfAPaymentBeingSettled(t *testing.T) {
	txB64, account := exacttest.SignedTx(t, nil)
	broadcasting, finish := make(chan struct{}), make(chan struct{})
	var held atomic.Bool
	node := &exacttest.Node{Account: account, Hash: "abc123", OnBroadcast: func() {
		if held.CompareAndSwap(false, true) {
			close(broadcasting)
			<-finish
		}
	}}
	f := exact.NewFacilitator(node, exacttest.ChainID)

	first := make(chan error, 1)
	go func() {
		_, err := settle(f, txB64)
		first <- err
	}()
	<-broadcasting

	_, err := settle(f, txB64)
	requireDuplicate(t, err)

	close(finish)
	require.NoError(t, <-first)
	assert.Equal(t, 1, node.Broadcasts)
	assert.Equal(t, 1, node.Simulates, "the copy is refused before it costs the node a simulation")
}

// TestSettle_RefusesACopyOfASettledPayment pins that a settled payment stays
// claimed. A node that has not yet committed it would still admit a copy, and
// answer that broadcast with the original's delivery.
func TestSettle_RefusesACopyOfASettledPayment(t *testing.T) {
	txB64, account := exacttest.SignedTx(t, nil)
	node := &exacttest.Node{Account: account, Hash: "abc123"}
	f := exact.NewFacilitator(node, exacttest.ChainID)

	_, err := settle(f, txB64)
	require.NoError(t, err)
	_, err = settle(f, txB64)

	requireDuplicate(t, err)
	assert.Equal(t, 1, node.Broadcasts)
}

// TestSettle_RefusesACopyOfAnAbortedDelivery covers the other committed outcome:
// a delivery the chain aborted consumed the sequence, so a copy cannot land
// either, and it is refused the same way.
func TestSettle_RefusesACopyOfAnAbortedDelivery(t *testing.T) {
	txB64, account := exacttest.SignedTx(t, nil)
	node := &exacttest.Node{Account: account, Hash: "abc123", BroadcastErr: std.ErrSessionExpired("session expired")}
	f := exact.NewFacilitator(node, exacttest.ChainID)

	_, err := settle(f, txB64)
	refusal, ok := errors.AsType[*x402.SettleError](err)
	require.True(t, ok, "want *x402.SettleError, got %v", err)
	require.Equal(t, exact.ReasonBroadcastFailed, refusal.ErrorReason)

	_, err = settle(f, txB64)
	requireDuplicate(t, err)
	assert.Equal(t, 1, node.Broadcasts)
}

// TestSettle_SettlesAgainAPaymentNothingWasBroadcastFor pins that a settle which
// broadcast nothing leaves the payment unclaimed, so it can be settled once the
// cause is gone rather than refused as a duplicate of nothing.
func TestSettle_SettlesAgainAPaymentNothingWasBroadcastFor(t *testing.T) {
	cases := map[string]struct {
		node       func(account exact.SignerAccount) *exacttest.Node
		broadcasts int
	}{
		"the account could not be read": {
			node: func(account exact.SignerAccount) *exacttest.Node {
				return &exacttest.Node{Account: account, AccountErr: errors.New("connection refused")}
			},
		},
		"the simulation refused it": {
			node: func(account exact.SignerAccount) *exacttest.Node {
				return &exacttest.Node{Account: account, SimulateErr: std.ErrInsufficientFunds("insufficient funds")}
			},
		},
		"CheckTx refused it": {
			node: func(account exact.SignerAccount) *exacttest.Node {
				return &exacttest.Node{Account: account, BroadcastErr: std.ErrInsufficientFee("insufficient fee")}
			},
			broadcasts: 2,
		},
		"the broadcast never reached the node": {
			node: func(account exact.SignerAccount) *exacttest.Node {
				return &exacttest.Node{Account: account, BroadcastErr: errors.New("connection refused")}
			},
			broadcasts: 2,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			txB64, account := exacttest.SignedTx(t, nil)
			node := tc.node(account)
			f := exact.NewFacilitator(node, exacttest.ChainID)

			_, first := settle(f, txB64)
			require.Error(t, first)
			_, second := settle(f, txB64)

			assert.Equal(t, first, second, "the second settle checks the payment again and fails the same way")
			assert.Equal(t, tc.broadcasts, node.Broadcasts)
		})
	}
}

// TestSettlements_ForgetASettledPaymentAfterTheTTL pins the retention bound. A
// settled payment's sequence is consumed, so once every node has committed it a
// copy is refused by the chain itself; the claim only has to outlast a lagging
// node, and keeping it forever would grow without bound.
func TestSettlements_ForgetASettledPaymentAfterTheTTL(t *testing.T) {
	settlements := exact.NewSettlements()
	settledAt := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	require.True(t, settlements.Claim("abc123", settledAt))
	settlements.MarkSettled("abc123", settledAt)

	assert.False(t, settlements.Claim("abc123", settledAt.Add(exact.SettlementTTL)),
		"a payment settled within the TTL stays claimed")
	assert.True(t, settlements.Claim("abc123", settledAt.Add(exact.SettlementTTL+time.Nanosecond)),
		"a payment settled longer ago than the TTL can be claimed again")
}
