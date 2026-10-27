package exact

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gnolang/gno/tm2/pkg/amino"
	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
	"github.com/gnolang/gno/tm2/pkg/crypto"
	"github.com/gnolang/gno/tm2/pkg/std"
)

// gasPricePath is the ABCI query for the gas price of the chain's last block.
const gasPricePath = "auth/gasprice"

var errNoRPCClient = errors.New("x402-gno: no RPC client to read the chain through")

// NewRPCNode returns the chain access a payment needs, read through rpc, a
// node's RPC client. Nothing is signed or broadcast through it: the scheme's
// Signer signs, and the seller's facilitator broadcasts. It holds no state
// beyond rpc, so one instance is safe to share across concurrent payments.
//
// The RPC client's timeout bounds every read, 60 seconds unless it was made
// with rpcclient.WithRequestTimeout, and so does the context each read is
// given. Nothing is signed until the reads have answered, so a read abandoned
// costs nothing.
func NewRPCNode(rpc rpcclient.Client) *RPCNode {
	return &RPCNode{rpc: rpc}
}

// RPCNode is the chain access NewRPCNode returns. It decodes accounts into
// tm2's own types rather than gno.land's, so importing this package links
// neither gno.land's node nor its VM.
type RPCNode struct {
	rpc rpcclient.Client
}

var _ Node = (*RPCNode)(nil)

// client is the RPC client to read through, or errNoRPCClient when there is
// none, a nil one inside the interface included.
func (n *RPCNode) client() (rpcclient.Client, error) {
	if n == nil || isNil(n.rpc) {
		return nil, errNoRPCClient
	}
	return n.rpc, nil
}

// NodeChainID is the chain id the node reports for itself.
func (n *RPCNode) NodeChainID(ctx context.Context) (string, error) {
	rpc, err := n.client()
	if err != nil {
		return "", err
	}
	status, err := rpc.Status(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("query node status: %w", err)
	}
	if status == nil {
		return "", errors.New("query node status: the node returned no result")
	}
	return status.NodeInfo.Network, nil
}

// query runs the ABCI query path and returns the data it answers with. A query
// the chain refused returns the chain's own abci.Error, wrapped, so a caller
// can tell what the chain decided by its type.
func (n *RPCNode) query(ctx context.Context, path string) ([]byte, error) {
	rpc, err := n.client()
	if err != nil {
		return nil, err
	}
	res, err := rpc.ABCIQuery(ctx, path, []byte{})
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", path, err)
	}
	if res == nil {
		return nil, fmt.Errorf("query %s: the node returned no result", path)
	}
	if res.Response.Error != nil {
		return nil, fmt.Errorf("query %s: %s: %w", path, res.Response.Log, res.Response.Error)
	}
	return res.Response.Data, nil
}

// Account reads addr's account. An address the chain holds no account for is
// std.ErrUnknownAddress.
func (n *RPCNode) Account(ctx context.Context, addr crypto.Address) (std.Account, error) {
	data, err := n.query(ctx, "auth/accounts/"+addr.String())
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || string(data) == "null" {
		return nil, std.ErrUnknownAddress("unknown address: " + addr.String())
	}
	// gno.land's account adds fields std's lacks, which amino refuses, so only
	// the std part is decoded.
	var gnoAccount struct{ BaseAccount json.RawMessage }
	if err := json.Unmarshal(data, &gnoAccount); err != nil {
		return nil, fmt.Errorf("decode the account of %s: %w", addr, err)
	}
	var acc std.BaseAccount
	if err := amino.UnmarshalJSON(gnoAccount.BaseAccount, &acc); err != nil {
		return nil, fmt.Errorf("decode the account of %s: %w", addr, err)
	}
	return &acc, nil
}

// SessionAccount reads the session account session holds under master. A
// session the chain does not hold, such as a revoked one, is the chain's
// std.SessionNotFoundError.
func (n *RPCNode) SessionAccount(ctx context.Context, master, session crypto.Address) (std.Account, error) {
	data, err := n.query(ctx, "auth/accounts/"+master.String()+"/session/"+session.String())
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || string(data) == "null" {
		return nil, std.ErrSessionNotFound("no session " + session.String() + " under " + master.String())
	}
	var gnoSession struct{ BaseSessionAccount json.RawMessage }
	if err := json.Unmarshal(data, &gnoSession); err != nil {
		return nil, fmt.Errorf("decode session %s of %s: %w", session, master, err)
	}
	var acc std.BaseSessionAccount
	if err := amino.UnmarshalJSON(gnoSession.BaseSessionAccount, &acc); err != nil {
		return nil, fmt.Errorf("decode session %s of %s: %w", session, master, err)
	}
	return &acc, nil
}

// GasPrice reads the gas price of the chain's last block the way gnokey reads
// it to estimate a fee. A chain that has stored no price answers gas 0 at an
// empty price, which is no price, as is an answer with nothing in it.
func (n *RPCNode) GasPrice(ctx context.Context) (*std.GasPrice, error) {
	data, err := n.query(ctx, gasPricePath)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || string(data) == "null" {
		return nil, nil
	}
	var price std.GasPrice
	if err := amino.UnmarshalJSON(data, &price); err != nil {
		return nil, fmt.Errorf("decode gas price %q: %w", data, err)
	}
	if price.Gas == 0 || price.Price.Amount == 0 {
		return nil, nil
	}
	return &price, nil
}
