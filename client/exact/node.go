package exact

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gnolang/gno/tm2/pkg/amino"
	abci "github.com/gnolang/gno/tm2/pkg/bft/abci/types"
	"github.com/gnolang/gno/tm2/pkg/crypto"
	"github.com/gnolang/gno/tm2/pkg/std"
)

// gasPricePath is the ABCI query for the gas price of the chain's last block.
const gasPricePath = "auth/gasprice"

const readTimeout = 60 * time.Second

// maxAnswerBytes caps one answer from the node. The answers a payment reads, a
// status, a gas price and one account, are each a few kilobytes.
const maxAnswerBytes = 1 << 20

var errNoNode = errors.New("x402-gno: no node to read the chain from")

// NewRPCNode returns the chain access a payment needs, read from the node whose
// RPC server listens at remote: an http(s) URL such as
// https://rpc.onyx.testnets.gno.land:443, a tcp:// address as a node's
// configuration writes it, or a bare host:port as gnokey's -remote takes it,
// the last two read over plain http. Nothing is signed or broadcast through
// it: the scheme's Signer signs, and the seller's facilitator broadcasts. It
// holds no state beyond the address and an HTTP client, so one instance is
// safe to share across concurrent payments.
//
// Every read is bounded by 60 seconds and by the context it is given, and an
// answer over 1 MiB is refused. Nothing is signed until the reads have
// answered, so a read abandoned costs nothing.
func NewRPCNode(remote string) (*RPCNode, error) {
	if rest, ok := strings.CutPrefix(remote, "tcp://"); ok {
		remote = "http://" + rest
	} else if !strings.Contains(remote, "://") {
		remote = "http://" + remote
	}
	// The remote is left out of the error: a refused one may carry a password
	// where no URL parser finds it to mask.
	u, err := url.Parse(remote)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, errors.New("x402-gno: a node's RPC address is an http(s) URL, a tcp:// address or a host:port, with a host")
	}
	// Each read appends its method to the address, which a query or a fragment
	// would swallow.
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, errors.New("x402-gno: a node's RPC address takes no query or fragment")
	}
	return &RPCNode{
		endpoint: strings.TrimSuffix(u.String(), "/"),
		shown:    strings.TrimSuffix(u.Redacted(), "/"),
		client:   &http.Client{Timeout: readTimeout},
	}, nil
}

// RPCNode is the chain access NewRPCNode returns. It speaks the node's JSON-RPC
// over plain HTTP and decodes accounts into tm2's own types, so importing this
// package links neither tm2's RPC client nor gno.land's node.
type RPCNode struct {
	endpoint string
	shown    string // endpoint with any password masked, for errors
	client   *http.Client
}

var _ Node = (*RPCNode)(nil)

// NodeChainID is the chain id the node reports for itself.
func (n *RPCNode) NodeChainID(ctx context.Context) (string, error) {
	result, err := n.call(ctx, "status", nil)
	if err != nil {
		return "", fmt.Errorf("query node status: %w", err)
	}
	var status struct {
		NodeInfo struct {
			Network string `json:"network"`
		} `json:"node_info"`
	}
	if err := json.Unmarshal(result, &status); err != nil {
		return "", fmt.Errorf("query node status: %w", err)
	}
	if status.NodeInfo.Network == "" {
		return "", errors.New("query node status: the node's status names no chain")
	}
	return status.NodeInfo.Network, nil
}

// query runs the ABCI query path and returns the data it answers with. A query
// the chain refused returns the chain's own abci.Error, wrapped, so a caller
// can tell what the chain decided by its type.
func (n *RPCNode) query(ctx context.Context, path string) ([]byte, error) {
	result, err := n.call(ctx, "abci_query", url.Values{"path": {strconv.Quote(path)}})
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", path, err)
	}
	var answer struct {
		Response abci.ResponseQuery `json:"response"`
	}
	if err := amino.UnmarshalJSON(result, &answer); err != nil {
		return nil, fmt.Errorf("query %s: decode the answer: %w", path, err)
	}
	if answer.Response.Error != nil {
		return nil, fmt.Errorf("query %s: %s: %w", path, answer.Response.Log, answer.Response.Error)
	}
	return answer.Response.Data, nil
}

// call makes one JSON-RPC call in its URI form, GET <endpoint>/<method>?<params>,
// and returns the answer's result.
func (n *RPCNode) call(ctx context.Context, method string, params url.Values) (json.RawMessage, error) {
	if n == nil {
		return nil, errNoNode
	}
	target := n.endpoint + "/" + method
	if len(params) > 0 {
		target += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	res, err := n.client.Do(req)
	if err != nil {
		// The client's error names the URL, with any password masked.
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, maxAnswerBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%s at %s: %w", method, n.shown, err)
	}
	if len(body) > maxAnswerBytes {
		return nil, fmt.Errorf("%s at %s: the answer is over %d bytes", method, n.shown, maxAnswerBytes)
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
			Data    string `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("%s at %s: status %d, not a JSON-RPC answer", method, n.shown, res.StatusCode)
	}
	if envelope.Error != nil {
		return nil, fmt.Errorf("%s at %s: %s", method, n.shown, strings.TrimSpace(envelope.Error.Message+" "+envelope.Error.Data))
	}
	// A node always answers these methods with a result, so one without is no
	// verdict: read as one, it would be an unfunded account or an unpriced chain.
	if len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return nil, fmt.Errorf("%s at %s: status %d, the answer holds no result", method, n.shown, res.StatusCode)
	}
	return envelope.Result, nil
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
	// An answer for no account, or for another, would have the payment signed
	// over a number and sequence the chain does not hold for addr.
	if acc.Address != addr {
		return nil, fmt.Errorf("decode the account of %s: the node answered for %q", addr, acc.Address)
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
	if acc.Address != session || acc.MasterAddress != master {
		return nil, fmt.Errorf("decode session %s of %s: the node answered for session %q of %q", session, master, acc.Address, acc.MasterAddress)
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
