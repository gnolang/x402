package exact_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	gnoclient "github.com/gnolang/gno/gno.land/pkg/gnoclient"
	"github.com/gnolang/gno/tm2/pkg/amino"
	abci "github.com/gnolang/gno/tm2/pkg/bft/abci/types"
	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
	ctypes "github.com/gnolang/gno/tm2/pkg/bft/rpc/core/types"
	bfttypes "github.com/gnolang/gno/tm2/pkg/bft/types"
	"github.com/gnolang/gno/tm2/pkg/std"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gnoverse/x402/facilitator/exact"
)

// TestGnoclientNode_NodeChainIDWithoutAClient pins the guard on the chain-id
// query an operator's startup check runs: a missing RPC client must refuse the
// lookup, not panic.
func TestGnoclientNode_NodeChainIDWithoutAClient(t *testing.T) {
	_, err := exact.NewGnoclientNode(&gnoclient.Client{}).NodeChainID(context.Background())
	require.ErrorIs(t, err, gnoclient.ErrMissingRPCClient)
}

// stubRPCClient answers a broadcast, a transaction lookup or a query and nothing
// else. The transport interface is a composite of seven, so it is embedded to
// supply the rest: any other call is a nil-interface panic naming the method the
// test did not expect.
type stubRPCClient struct {
	rpcclient.Client
	broadcastBytes func(bfttypes.Tx) // sees the bytes broadcast, when set
	broadcast      func() (*ctypes.ResultBroadcastTx, error)
	tx             func(hash []byte) (*ctypes.ResultTx, error)
	abciQuery      func() (*ctypes.ResultABCIQuery, error)
}

func (s stubRPCClient) BroadcastTxSync(_ context.Context, tx bfttypes.Tx) (*ctypes.ResultBroadcastTx, error) {
	if s.broadcastBytes != nil {
		s.broadcastBytes(tx)
	}
	return s.broadcast()
}

func (s stubRPCClient) Tx(_ context.Context, hash []byte) (*ctypes.ResultTx, error) {
	return s.tx(hash)
}

func (s stubRPCClient) ABCIQuery(context.Context, string, []byte) (*ctypes.ResultABCIQuery, error) {
	return s.abciQuery()
}

// onyxSimulateFixture is the JSON-RPC body onyx-1 returned for an abci_query
// .app/simulate of a 1ugnot bank send. Its delivery carries a bank.TransferEvent.
const onyxSimulateFixture = "onyx_simulate_send.json"

// TestGnoclientNode_SimulateDecodesOnyxReply pins the gno client to the chain the
// facilitator serves: a reply it cannot decode is not the chain's verdict, so
// every payment would be answered 503.
func TestGnoclientNode_SimulateDecodesOnyxReply(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", onyxSimulateFixture))
	require.NoError(t, err)
	var body struct {
		Result json.RawMessage `json:"result"`
	}
	require.NoError(t, json.Unmarshal(raw, &body))
	var res ctypes.ResultABCIQuery
	require.NoError(t, amino.UnmarshalJSON(body.Result, &res))

	node := exact.NewGnoclientNode(&gnoclient.Client{RPCClient: stubRPCClient{
		abciQuery: func() (*ctypes.ResultABCIQuery, error) { return &res, nil },
	}})

	require.NoError(t, node.Simulate(&std.Tx{}))
}

func broadcastThrough(t *testing.T, res *ctypes.ResultBroadcastTx, err error) (string, error) {
	t.Helper()
	node := exact.NewGnoclientNode(&gnoclient.Client{RPCClient: stubRPCClient{
		broadcast: func() (*ctypes.ResultBroadcastTx, error) { return res, err },
	}})
	return node.Broadcast([]byte{0x0a})
}

// TestGnoclientNode_BroadcastReportsTheHashOfAnAdmittedTx pins what a nil error
// means: the node's CheckTx admitted the transaction to its mempool, under the
// hash it answers with.
func TestGnoclientNode_BroadcastReportsTheHashOfAnAdmittedTx(t *testing.T) {
	hash, err := broadcastThrough(t, &ctypes.ResultBroadcastTx{Hash: []byte{0xde, 0xad, 0xbe, 0xef}}, nil)

	require.NoError(t, err)
	assert.Equal(t, "deadbeef", hash)
}

// TestGnoclientNode_BroadcastReportsNoTxWhenTheChainNeverAnswered pins that a
// transport failure carries no result, so there is no hash to report and nothing
// about the transaction is known.
func TestGnoclientNode_BroadcastReportsNoTxWhenTheChainNeverAnswered(t *testing.T) {
	hash, err := broadcastThrough(t, nil, errors.New("connection refused"))

	require.Error(t, err)
	assert.False(t, exact.ChainRefused(err), "an unreachable node refuses nothing")
	assert.Empty(t, hash)
}

// TestGnoclientNode_BroadcastReportsNoTxForACheckTxRefusal pins that a
// transaction CheckTx refused reports no hash. It never entered a mempool, so
// nothing was broadcast and nothing can be reconciled, although the node answers
// with its hash all the same.
func TestGnoclientNode_BroadcastReportsNoTxForACheckTxRefusal(t *testing.T) {
	hash, err := broadcastThrough(t, &ctypes.ResultBroadcastTx{
		Error: std.InsufficientFundsError{},
		Hash:  []byte{0xde, 0xad, 0xbe, 0xef},
	}, nil)

	require.Error(t, err)
	assert.True(t, exact.ChainRefused(err), "a CheckTx refusal is the chain's own answer")
	assert.Empty(t, hash, "a transaction CheckTx refused was never broadcast")
}

// TestGnoclientNode_BroadcastSendsTheBytesItIsGiven pins the adapter's half of the
// relay invariant: the bytes reach the node unchanged.
func TestGnoclientNode_BroadcastSendsTheBytesItIsGiven(t *testing.T) {
	sent := []byte{0x0a, 0x01, 0x02}
	var received []byte
	node := exact.NewGnoclientNode(&gnoclient.Client{RPCClient: stubRPCClient{
		broadcastBytes: func(tx bfttypes.Tx) { received = tx },
		broadcast: func() (*ctypes.ResultBroadcastTx, error) {
			return &ctypes.ResultBroadcastTx{Hash: []byte{0x01}}, nil
		},
	}})

	_, err := node.Broadcast(sent)
	require.NoError(t, err)
	assert.Equal(t, sent, []byte(received))
}

// lookups answers each transaction lookup with the next of answers, the last one
// repeating, and records the hashes it was asked about.
type lookups struct {
	answers []func() (*ctypes.ResultTx, error)
	asked   [][]byte
}

func (l *lookups) node() *exact.GnoclientNode {
	return exact.NewGnoclientNode(&gnoclient.Client{RPCClient: stubRPCClient{
		tx: func(hash []byte) (*ctypes.ResultTx, error) {
			l.asked = append(l.asked, hash)
			answer := l.answers[min(len(l.asked), len(l.answers))-1]
			return answer()
		},
	}})
}

func delivered() (*ctypes.ResultTx, error) { return &ctypes.ResultTx{Height: 12}, nil }

func notFound() (*ctypes.ResultTx, error) {
	return nil, errors.New("no transaction result for hash deadbeef")
}

// TestGnoclientNode_AwaitDeliveryReportsADeliveredTx pins the lookup a delivery
// is read from: the transaction the hash names, which the node keeps in its
// state database whether or not it indexes transactions.
func TestGnoclientNode_AwaitDeliveryReportsADeliveredTx(t *testing.T) {
	l := &lookups{answers: []func() (*ctypes.ResultTx, error){delivered}}

	require.NoError(t, l.node().AwaitDelivery(t.Context(), "deadbeef"))
	assert.Equal(t, [][]byte{{0xde, 0xad, 0xbe, 0xef}}, l.asked)
}

// TestGnoclientNode_AwaitDeliveryReportsAnAbortedDelivery pins that a block that
// executed and aborted the transaction is the chain's own answer.
func TestGnoclientNode_AwaitDeliveryReportsAnAbortedDelivery(t *testing.T) {
	l := &lookups{answers: []func() (*ctypes.ResultTx, error){func() (*ctypes.ResultTx, error) {
		return &ctypes.ResultTx{TxResult: abci.ResponseDeliverTx{
			ResponseBase: abci.ResponseBase{Error: std.SessionExpiredError{}},
		}}, nil
	}}}

	err := l.node().AwaitDelivery(t.Context(), "deadbeef")

	require.Error(t, err)
	assert.True(t, exact.ChainRefused(err), "an aborted delivery is the chain's own answer")
}

// TestGnoclientNode_AwaitDeliveryAsksUntilABlockHoldsIt pins the wait: a lookup
// that finds nothing yet is asked again rather than answered.
func TestGnoclientNode_AwaitDeliveryAsksUntilABlockHoldsIt(t *testing.T) {
	l := &lookups{answers: []func() (*ctypes.ResultTx, error){notFound, delivered}}

	require.NoError(t, l.node().AwaitDelivery(t.Context(), "deadbeef"))
	assert.Len(t, l.asked, 2)
}

// TestGnoclientNode_AwaitDeliveryEndsWithItsContext pins that a transaction no
// block holds is waited for only as long as the caller allows, and that running
// out is not the chain refusing it.
func TestGnoclientNode_AwaitDeliveryEndsWithItsContext(t *testing.T) {
	l := &lookups{answers: []func() (*ctypes.ResultTx, error){notFound}}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	err := l.node().AwaitDelivery(ctx, "deadbeef")

	require.Error(t, err)
	assert.False(t, exact.ChainRefused(err), "a transaction not found yet is not refused")
	assert.ErrorContains(t, err, "no transaction result", "the last answer says why")
}
