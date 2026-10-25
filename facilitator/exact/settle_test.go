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

	"github.com/gnolang/x402/facilitator/exact"
	"github.com/gnolang/x402/facilitator/exact/exacttest"
)

func settle(f *exact.Facilitator, txB64 string) (*x402.SettleResponse, error) {
	return settleWithin(context.Background(), f, txB64)
}

func settleWithin(ctx context.Context, f *exact.Facilitator, txB64 string) (*x402.SettleResponse, error) {
	return f.Settle(ctx, exacttest.PaymentPayload(exacttest.Requirements(), txB64), exacttest.Requirements(), nil)
}

// answerDeadline is a request deadline 200 ms past the margin a settle keeps
// before it, so the settle waits 200 ms for a delivery.
func answerDeadline(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), exact.DeliveryMargin+200*time.Millisecond)
	t.Cleanup(cancel)
	return ctx
}

func requirePending(t *testing.T, err error) {
	t.Helper()
	refusal, ok := errors.AsType[*x402.SettleError](err)
	require.True(t, ok, "want *x402.SettleError, got %v", err)
	assert.Equal(t, x402.ErrSettlementPending, refusal.ErrorReason)
	assert.Equal(t, "abc123", refusal.Transaction, "a pending settlement carries the hash it broadcast")
	assert.Equal(t, exacttest.Payer, refusal.Payer)
}

// requireDuplicate asserts err refuses a copy of a payment, carrying the hash a
// node admitted the payment under, or none while nothing says it was admitted.
func requireDuplicate(t *testing.T, err error, hash string) {
	t.Helper()
	refusal, ok := errors.AsType[*x402.SettleError](err)
	require.True(t, ok, "want *x402.SettleError, got %v", err)
	assert.Equal(t, exact.ReasonDuplicateSettlement, refusal.ErrorReason)
	assert.Empty(t, refusal.Payer, "a copy refused before its signature was checked names no payer")
	assert.Equal(t, hash, refusal.Transaction)
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
	requireDuplicate(t, err, "")

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

	requireDuplicate(t, err, "abc123")
	assert.Equal(t, 1, node.Broadcasts)
}

// TestSettle_RefusesACopyOfAnAbortedDelivery covers the other committed outcome:
// a delivery the chain aborted consumed the sequence, so a copy cannot land
// either, and it is refused the same way.
func TestSettle_RefusesACopyOfAnAbortedDelivery(t *testing.T) {
	txB64, account := exacttest.SignedTx(t, nil)
	node := &exacttest.Node{Account: account, Hash: "abc123", Delivery: func(context.Context) error {
		return std.ErrSessionExpired("session expired")
	}}
	f := exact.NewFacilitator(node, exacttest.ChainID)

	_, err := settle(f, txB64)
	refusal, ok := errors.AsType[*x402.SettleError](err)
	require.True(t, ok, "want *x402.SettleError, got %v", err)
	require.Equal(t, exact.ReasonBroadcastFailed, refusal.ErrorReason)
	require.Equal(t, "abc123", refusal.Transaction, "an aborted delivery charged the fee, so its hash is reported")

	_, err = settle(f, txB64)
	requireDuplicate(t, err, "abc123")
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

// TestSettle_ReportsAnUndeliveredBroadcastAsPending pins the answer for a payment
// the node admitted and no block executed while the settle waited. It may still
// commit, so it is not refused, and a 503 would leave the seller without the hash
// to reconcile against. x402 v2 names the answer settlement_pending, and the
// SDKs settle once more on it. The answer comes before the request's own
// deadline, or the server's timeout would answer in its place with nothing.
func TestSettle_ReportsAnUndeliveredBroadcastAsPending(t *testing.T) {
	txB64, account := exacttest.SignedTx(t, nil)
	node := &exacttest.Node{Account: account, Hash: "abc123", Delivery: exacttest.NeverDelivered}
	ctx := answerDeadline(t)

	_, err := settleWithin(ctx, exact.NewFacilitator(node, exacttest.ChainID), txB64)

	requirePending(t, err)
	assert.NoError(t, ctx.Err(), "the answer must come before the request's deadline")
	assert.Equal(t, "abc123", node.Awaited)
}

// TestSettle_ReconcilesAPendingPaymentWithoutBroadcastingAgain settles a pending
// payment again, as the seller's SDK does. It asks the chain about the hash it
// already broadcast instead of checking and broadcasting the payment again: the
// account check would now refuse a payment that landed as spent.
func TestSettle_ReconcilesAPendingPaymentWithoutBroadcastingAgain(t *testing.T) {
	txB64, account := exacttest.SignedTx(t, nil)
	delivered := false
	node := &exacttest.Node{Account: account, Hash: "abc123", Delivery: func(ctx context.Context) error {
		if delivered {
			return nil
		}
		return exacttest.NeverDelivered(ctx)
	}}
	f := exact.NewFacilitator(node, exacttest.ChainID)
	_, err := settleWithin(answerDeadline(t), f, txB64)
	requirePending(t, err)

	delivered = true
	res, err := settle(f, txB64)

	require.NoError(t, err)
	assert.True(t, res.Success)
	assert.Equal(t, "abc123", res.Transaction)
	assert.Equal(t, exacttest.Payer, res.Payer)
	assert.Equal(t, 1, node.Broadcasts, "a pending payment is never broadcast twice")
	assert.Equal(t, 1, node.Simulates, "nor checked again")
	assert.Equal(t, 2, node.Awaits)

	_, err = settle(f, txB64)
	requireDuplicate(t, err, "abc123")
}

// TestSettle_ReconcilesAPendingPaymentOnlyUnderItsOwnTerms settles a pending
// payment again under requirements its transaction does not pay. The offline
// rules refuse it before the chain is asked about the hash, and the payment
// stays pending for a settle under its own terms.
func TestSettle_ReconcilesAPendingPaymentOnlyUnderItsOwnTerms(t *testing.T) {
	txB64, account := exacttest.SignedTx(t, nil)
	delivered := false
	node := &exacttest.Node{Account: account, Hash: "abc123", Delivery: func(ctx context.Context) error {
		if delivered {
			return nil
		}
		return exacttest.NeverDelivered(ctx)
	}}
	f := exact.NewFacilitator(node, exacttest.ChainID)
	_, err := settleWithin(answerDeadline(t), f, txB64)
	requirePending(t, err)
	delivered = true

	cheaper := exacttest.Requirements()
	cheaper.Amount = "1"
	_, err = f.Settle(t.Context(), exacttest.PaymentPayload(cheaper, txB64), cheaper, nil)

	refusal, ok := errors.AsType[*x402.SettleError](err)
	require.True(t, ok, "want *x402.SettleError, got %v", err)
	assert.Equal(t, exact.ReasonAmountMismatch, refusal.ErrorReason)
	assert.Equal(t, 1, node.Awaits, "a payment refused offline never reaches the node")

	res, err := settle(f, txB64)
	require.NoError(t, err)
	assert.Equal(t, "abc123", res.Transaction)
}

// TestSettle_RefusesACopyOfAPaymentBeingReconciled holds a pending payment's
// reconcile inside its wait and settles a copy meanwhile. The copy is refused,
// and carries the hash the payment is pending under, so a seller whose retry
// lost the race still has the hash to give the buyer.
func TestSettle_RefusesACopyOfAPaymentBeingReconciled(t *testing.T) {
	txB64, account := exacttest.SignedTx(t, nil)
	var awaits atomic.Int32
	waiting, finish := make(chan struct{}), make(chan struct{})
	node := &exacttest.Node{Account: account, Hash: "abc123", Delivery: func(ctx context.Context) error {
		if awaits.Add(1) == 1 {
			return exacttest.NeverDelivered(ctx)
		}
		close(waiting)
		<-finish
		return nil
	}}
	f := exact.NewFacilitator(node, exacttest.ChainID)
	_, err := settleWithin(answerDeadline(t), f, txB64)
	requirePending(t, err)

	reconciled := make(chan error, 1)
	go func() {
		_, err := settle(f, txB64)
		reconciled <- err
	}()
	<-waiting

	_, err = settle(f, txB64)
	requireDuplicate(t, err, "abc123")

	close(finish)
	require.NoError(t, <-reconciled)
}

// TestSettle_ReconcilesAPendingPaymentTheChainAborted pins the other outcome a
// block can give a pending payment: its refusal carries the hash, as any aborted
// delivery's does.
func TestSettle_ReconcilesAPendingPaymentTheChainAborted(t *testing.T) {
	txB64, account := exacttest.SignedTx(t, nil)
	aborted := false
	node := &exacttest.Node{Account: account, Hash: "abc123", Delivery: func(ctx context.Context) error {
		if aborted {
			return std.ErrSessionExpired("session expired")
		}
		return exacttest.NeverDelivered(ctx)
	}}
	f := exact.NewFacilitator(node, exacttest.ChainID)
	_, err := settleWithin(answerDeadline(t), f, txB64)
	requirePending(t, err)

	aborted = true
	_, err = settle(f, txB64)

	refusal, ok := errors.AsType[*x402.SettleError](err)
	require.True(t, ok, "want *x402.SettleError, got %v", err)
	assert.Equal(t, exact.ReasonBroadcastFailed, refusal.ErrorReason)
	assert.Equal(t, "abc123", refusal.Transaction)
	assert.Equal(t, 1, node.Broadcasts)
}

// TestSettle_KeepsAPaymentPendingUntilABlockExecutesIt settles a pending payment
// again while still no block has executed it. It stays pending, carrying the same
// hash, and is still not broadcast again.
func TestSettle_KeepsAPaymentPendingUntilABlockExecutesIt(t *testing.T) {
	txB64, account := exacttest.SignedTx(t, nil)
	node := &exacttest.Node{Account: account, Hash: "abc123", Delivery: exacttest.NeverDelivered}
	f := exact.NewFacilitator(node, exacttest.ChainID)
	_, err := settleWithin(answerDeadline(t), f, txB64)
	requirePending(t, err)

	_, err = settleWithin(answerDeadline(t), f, txB64)

	requirePending(t, err)
	assert.Equal(t, 1, node.Broadcasts)
}

// TestSettlements_ForgetASettledPaymentAfterTheTTL pins the retention bound. A
// settled payment's sequence is consumed, so once every node has committed it a
// copy is refused by the chain itself; the claim only has to outlast a lagging
// node, and keeping it forever would grow without bound.
func TestSettlements_ForgetASettledPaymentAfterTheTTL(t *testing.T) {
	settlements := exact.NewSettlements()
	settledAt := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	_, ok := settlements.Claim("abc123", settledAt)
	require.True(t, ok)
	settlements.MarkSettled("abc123", settledAt)

	_, ok = settlements.Claim("abc123", settledAt.Add(exact.SettlementTTL))
	assert.False(t, ok, "a payment settled within the TTL stays claimed")
	_, ok = settlements.Claim("abc123", settledAt.Add(exact.SettlementTTL+time.Nanosecond))
	assert.True(t, ok, "a payment settled longer ago than the TTL can be claimed again")
}

// TestSettlements_ForgetAPendingPaymentTheTTLAfterItsBroadcast pins that settling
// a pending payment again does not extend its record. A transaction that left
// every mempool, as a restarted node's does, would otherwise stay pending for as
// long as the payment kept being presented, and never be checked and broadcast
// again.
func TestSettlements_ForgetAPendingPaymentTheTTLAfterItsBroadcast(t *testing.T) {
	settlements := exact.NewSettlements()
	broadcastAt := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	_, ok := settlements.Claim("abc123", broadcastAt)
	require.True(t, ok)
	settlements.MarkPending("abc123", broadcastAt)

	again := broadcastAt.Add(exact.SettlementTTL - time.Minute)
	pending, ok := settlements.Claim("abc123", again)
	require.True(t, ok)
	require.True(t, pending, "within the TTL the payment is reconciled")
	settlements.MarkPending("abc123", again)

	pending, ok = settlements.Claim("abc123", broadcastAt.Add(exact.SettlementTTL+time.Nanosecond))
	require.True(t, ok)
	assert.False(t, pending, "past the TTL from its broadcast the payment is settled afresh")
}
