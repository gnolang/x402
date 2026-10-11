package exact_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

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

// stubRPCClient answers a broadcast or a query and nothing else. The transport
// interface is a composite of seven, so it is embedded to supply the rest: any
// other call is a nil-interface panic naming the method the test did not expect.
type stubRPCClient struct {
	rpcclient.Client
	broadcast func() (*ctypes.ResultBroadcastTxCommit, error)
	abciQuery func() (*ctypes.ResultABCIQuery, error)
}

func (s stubRPCClient) BroadcastTxCommit(context.Context, bfttypes.Tx) (*ctypes.ResultBroadcastTxCommit, error) {
	return s.broadcast()
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

func broadcastThrough(t *testing.T, res *ctypes.ResultBroadcastTxCommit, err error) (string, error) {
	t.Helper()
	node := exact.NewGnoclientNode(&gnoclient.Client{RPCClient: stubRPCClient{
		broadcast: func() (*ctypes.ResultBroadcastTxCommit, error) { return res, err },
	}})
	return node.Broadcast(&std.Tx{})
}

// TestGnoclientNode_BroadcastReportsTheTxTheChainAborted keeps the transaction
// hash of a delivery the chain committed and then refused. Such a transaction
// exists on chain and charged the payer its fee, so the hash is the one record
// an operator can reconcile against — and upstream returns the result alongside
// the error precisely for the CheckTx and DeliverTx paths.
func TestGnoclientNode_BroadcastReportsTheTxTheChainAborted(t *testing.T) {
	hash, err := broadcastThrough(t, &ctypes.ResultBroadcastTxCommit{
		DeliverTx: abci.ResponseDeliverTx{
			ResponseBase: abci.ResponseBase{Error: std.SessionExpiredError{}},
		},
		Hash:   []byte{0xde, 0xad, 0xbe, 0xef},
		Height: 12,
	}, nil)

	require.Error(t, err)
	assert.True(t, exact.ChainRefused(err), "an aborted delivery is the chain's own answer")
	assert.Equal(t, "deadbeef", hash)
}

// TestGnoclientNode_BroadcastReportsNoTxWhenTheChainNeverAnswered pins the other
// half: a transport failure carries no result, so there is no hash to report and
// nothing about the transaction is known.
func TestGnoclientNode_BroadcastReportsNoTxWhenTheChainNeverAnswered(t *testing.T) {
	hash, err := broadcastThrough(t, nil, errors.New("connection refused"))

	require.Error(t, err)
	assert.False(t, exact.ChainRefused(err), "an unreachable node refuses nothing")
	assert.Empty(t, hash)
}
