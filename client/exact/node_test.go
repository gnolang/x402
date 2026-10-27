package exact_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	abci "github.com/gnolang/gno/tm2/pkg/bft/abci/types"
	ctypes "github.com/gnolang/gno/tm2/pkg/bft/rpc/core/types"
	rpctypes "github.com/gnolang/gno/tm2/pkg/bft/rpc/lib/types"
	"github.com/gnolang/gno/tm2/pkg/crypto"
	p2pTypes "github.com/gnolang/gno/tm2/pkg/p2p/types"
	"github.com/gnolang/gno/tm2/pkg/std"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	x402 "github.com/x402-foundation/x402/go/v2"

	"github.com/gnolang/x402/client/exact"
)

// The chain's answers, as gno.land writes them: amino JSON, indented. The gas
// price is what onyx-1 answered on 2026-10-22; the unset one is what a chain
// whose genesis stores no price answers.
const (
	onyxGasPrice  = "{\n  \"gas\": \"1000\",\n  \"price\": \"1ugnot\"\n}"
	unsetGasPrice = "{\n  \"gas\": \"0\",\n  \"price\": \"\"\n}"

	accountJSON = `{
  "BaseAccount": {
    "address": "g1jg8mtutu9khhfwc4nxmuhcpftf0pajdhfvsqf5",
    "coins": "1000ugnot",
    "public_key": null,
    "account_number": "12",
    "sequence": "7"
  },
  "attributes": "0"
}`

	sessionJSON = `{
  "BaseSessionAccount": {
    "BaseAccount": {
      "address": "g1u7y667z64x2h7vc6fmpcprgey4ck233jaww9zq",
      "coins": "",
      "public_key": null,
      "account_number": "34",
      "sequence": "3"
    },
    "master_address": "g1jg8mtutu9khhfwc4nxmuhcpftf0pajdhfvsqf5",
    "expires_at": "0"
  }
}`
)

// stubRPC is a node's JSON-RPC endpoint answering the two methods a payment
// reads, status and abci_query by path, encoded as tm2's RPC server encodes
// them. A path it has no answer for answers with no data, and any other call
// fails the test.
type stubRPC struct {
	answers map[string]abci.ResponseQuery
	status  *ctypes.ResultStatus
	rpcErr  error  // answer every call with this JSON-RPC error
	raw     []byte // answer every call with these bytes instead

	mu    sync.Mutex
	paths []string // the ABCI path each query named
}

func (s *stubRPC) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.raw != nil {
		_, _ = w.Write(s.raw)
		return
	}
	var result any
	switch r.URL.Path {
	case "/status":
		result = s.status
	case "/abci_query":
		path, err := strconv.Unquote(r.URL.Query().Get("path"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.paths = append(s.paths, path)
		s.mu.Unlock()
		result = &ctypes.ResultABCIQuery{Response: s.answers[path]}
	default:
		http.NotFound(w, r)
		return
	}
	res := rpctypes.NewRPCSuccessResponse(rpctypes.JSONRPCStringID(""), result)
	if s.rpcErr != nil {
		res = rpctypes.RPCInternalError(rpctypes.JSONRPCStringID(""), s.rpcErr)
	}
	_ = json.NewEncoder(w).Encode(res)
}

func (s *stubRPC) queried() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.paths
}

func answering(data string) abci.ResponseQuery {
	return abci.ResponseQuery{ResponseBase: abci.ResponseBase{Data: []byte(data)}}
}

func nodeOver(t *testing.T, rpc *stubRPC) *exact.RPCNode {
	t.Helper()
	server := httptest.NewServer(rpc)
	t.Cleanup(server.Close)
	node, err := exact.NewRPCNode(server.URL)
	require.NoError(t, err)
	return node
}

// TestTheGasPriceIsReadAsTheChainWritesIt decodes a live chain's answer, the
// one a payment's fee on onyx-1 is computed from.
func TestTheGasPriceIsReadAsTheChainWritesIt(t *testing.T) {
	rpc := &stubRPC{answers: map[string]abci.ResponseQuery{"auth/gasprice": answering(onyxGasPrice)}}

	price, err := nodeOver(t, rpc).GasPrice(context.Background())

	require.NoError(t, err)
	assert.Equal(t, &std.GasPrice{Gas: 1000, Price: std.Coin{Denom: "ugnot", Amount: 1}}, price)
	assert.Equal(t, []string{"auth/gasprice"}, rpc.queried())
}

// TestANodeAnsweringNoGasPriceReportsNone: a chain that stored no price
// answers gas 0 at an empty price, which is no price, as are no data and JSON
// null; the scheme answers each with its unpriced fee.
func TestANodeAnsweringNoGasPriceReportsNone(t *testing.T) {
	for name, data := range map[string]string{"an unset price": unsetGasPrice, "no data": "", "null": "null"} {
		t.Run(name, func(t *testing.T) {
			rpc := &stubRPC{answers: map[string]abci.ResponseQuery{"auth/gasprice": answering(data)}}

			price, err := nodeOver(t, rpc).GasPrice(context.Background())

			require.NoError(t, err)
			assert.Nil(t, price)
		})
	}
}

// TestAGasPriceTheNodeCannotGiveIsAnError keeps five failures apart from a
// chain reporting no price: a refused query, an RPC error, an answer too large
// to read, an answer that is not a gas price, and a node that never answered.
// Pricing a payment at the unpriced fee on any of them would guess where the
// chain did not say.
func TestAGasPriceTheNodeCannotGiveIsAnError(t *testing.T) {
	refused := std.UnknownRequestError{}
	cases := map[string]struct {
		rpc     *stubRPC
		wantIs  error
		wantMsg string
	}{
		"a refusal": {
			rpc:    &stubRPC{answers: map[string]abci.ResponseQuery{"auth/gasprice": {ResponseBase: abci.ResponseBase{Error: refused, Log: "unknown request"}}}},
			wantIs: refused,
		},
		"an RPC error": {
			rpc:     &stubRPC{rpcErr: errors.New("height must be at least 1")},
			wantMsg: "height must be at least 1",
		},
		"an answer over a mebibyte": {
			rpc:     &stubRPC{raw: bytes.Repeat([]byte(" "), 1<<20+1)},
			wantMsg: "over 1048576 bytes",
		},
		"an answer that is no gas price": {
			rpc: &stubRPC{answers: map[string]abci.ResponseQuery{"auth/gasprice": answering(`{"gas": "lots"}`)}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := nodeOver(t, tc.rpc).GasPrice(context.Background())

			require.Error(t, err)
			if tc.wantIs != nil {
				assert.ErrorIs(t, err, tc.wantIs)
			}
			assert.ErrorContains(t, err, tc.wantMsg)
		})
	}

	t.Run("no answer", func(t *testing.T) {
		server := httptest.NewServer(&stubRPC{})
		node, err := exact.NewRPCNode(server.URL)
		require.NoError(t, err)
		server.Close()

		_, err = node.GasPrice(context.Background())

		require.Error(t, err)
	})
}

// TestAccountsAreReadAsTheChainWritesThem decodes an account and a session
// account in the form a gno.land node gives them.
func TestAccountsAreReadAsTheChainWritesThem(t *testing.T) {
	master := crypto.MustAddressFromString(test1Address)
	session := crypto.MustAddressFromString(payTo)
	rpc := &stubRPC{answers: map[string]abci.ResponseQuery{
		"auth/accounts/" + test1Address:                       answering(accountJSON),
		"auth/accounts/" + test1Address + "/session/" + payTo: answering(sessionJSON),
	}}
	node := nodeOver(t, rpc)

	acc, err := node.Account(context.Background(), master)
	require.NoError(t, err)
	assert.Equal(t, uint64(12), acc.GetAccountNumber())
	assert.Equal(t, uint64(7), acc.GetSequence())

	sess, err := node.SessionAccount(context.Background(), master, session)
	require.NoError(t, err)
	assert.Equal(t, uint64(34), sess.GetAccountNumber())
	assert.Equal(t, uint64(3), sess.GetSequence())
}

// TestFieldsOnlyGnoLandDefinesAreIgnored: gno.land adds attributes to an
// account and allowed paths to a session; a payment reads neither, and their
// presence must not stop the number and sequence from decoding.
func TestFieldsOnlyGnoLandDefinesAreIgnored(t *testing.T) {
	master := crypto.MustAddressFromString(test1Address)
	session := crypto.MustAddressFromString(payTo)
	rpc := &stubRPC{answers: map[string]abci.ResponseQuery{
		"auth/accounts/" + test1Address: answering(strings.Replace(accountJSON, `"attributes": "0"`, `"attributes": "1"`, 1)),
		"auth/accounts/" + test1Address + "/session/" + payTo: answering(`{"BaseSessionAccount": {"BaseAccount": {"address": "` + payTo +
			`", "coins": "", "public_key": null, "account_number": "34", "sequence": "3"}, "master_address": "` + test1Address +
			`", "expires_at": "0"}, "allow_paths": ["gno.land/r/demo/"]}`),
	}}
	node := nodeOver(t, rpc)

	acc, err := node.Account(context.Background(), master)
	require.NoError(t, err)
	assert.Equal(t, [2]uint64{12, 7}, [2]uint64{acc.GetAccountNumber(), acc.GetSequence()})

	sess, err := node.SessionAccount(context.Background(), master, session)
	require.NoError(t, err)
	assert.Equal(t, [2]uint64{34, 3}, [2]uint64{sess.GetAccountNumber(), sess.GetSequence()})
}

// TestAnUnfundedAccountAndARevokedSessionReadDifferently: the remedies
// differ — fund the address, or register the session again — so the errors
// do too. An account the chain holds none of is std.UnknownAddressError; a
// session the chain refuses to find is its std.SessionNotFoundError.
func TestAnUnfundedAccountAndARevokedSessionReadDifferently(t *testing.T) {
	master := crypto.MustAddressFromString(test1Address)
	session := crypto.MustAddressFromString(payTo)
	rpc := &stubRPC{answers: map[string]abci.ResponseQuery{
		"auth/accounts/" + test1Address + "/session/" + payTo: {ResponseBase: abci.ResponseBase{
			Error: std.SessionNotFoundError{}, Log: "session not found",
		}},
	}}
	node := nodeOver(t, rpc)

	_, err := node.Account(context.Background(), master)
	_, unknown := errors.AsType[std.UnknownAddressError](err)
	assert.True(t, unknown, "got %v", err)

	_, err = node.SessionAccount(context.Background(), master, session)
	_, notFound := errors.AsType[std.SessionNotFoundError](err)
	assert.True(t, notFound, "got %v", err)
	_, unknown = errors.AsType[std.UnknownAddressError](err)
	assert.False(t, unknown, "a revoked session is not an address to fund")
}

// TestAReadStopsWhenItsCallerGivesUp: the context reaches the RPC client, so a
// payment whose caller has given up reads nothing more.
func TestAReadStopsWhenItsCallerGivesUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rpc := &stubRPC{answers: map[string]abci.ResponseQuery{"auth/accounts/" + test1Address: answering(accountJSON)}}

	_, err := nodeOver(t, rpc).Account(ctx, crypto.MustAddressFromString(test1Address))

	require.ErrorIs(t, err, context.Canceled)
}

// TestAPaymentGivenUpDuringItsReadsIsNotSigned: the caller's context ends
// while the account is being read, and the payment is refused before the
// signer is asked: a signature is the one step that cannot be taken back.
func TestAPaymentGivenUpDuringItsReadsIsNotSigned(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	signer := &countingSigner{Signer: signerFrom(t, test1Mnemonic)}
	node := newFakeNode()
	node.onAccount = cancel

	_, err := exact.NewExactGnoScheme(signer, node).CreatePaymentPayload(ctx, offer(), x402.PaymentPayloadContext{})

	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, signer.signs)
}

// TestTheNodeNamesTheChainItServes reads the chain id from the node's status,
// the same field the facilitator checks its own configuration against.
func TestTheNodeNamesTheChainItServes(t *testing.T) {
	rpc := &stubRPC{status: &ctypes.ResultStatus{NodeInfo: p2pTypes.NodeInfo{Network: "onyx-1"}}}

	served, err := nodeOver(t, rpc).NodeChainID(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "onyx-1", served)
}

// TestANodeIsReachedByItsHTTPAddress: the node is named by the http(s) URL its
// RPC server listens at, a trailing slash included. A gnokey-style host:port,
// a tcp:// address or a URL with no host names nothing a GET can reach, so
// the node refuses it before any payment reads through it.
func TestANodeIsReachedByItsHTTPAddress(t *testing.T) {
	for _, remote := range []string{"", "127.0.0.1:26657", "tcp://127.0.0.1:26657", "ftp://rpc.example", "http://"} {
		_, err := exact.NewRPCNode(remote)
		assert.Error(t, err, "%q", remote)
	}

	rpc := &stubRPC{answers: map[string]abci.ResponseQuery{"auth/gasprice": answering(onyxGasPrice)}}
	server := httptest.NewServer(rpc)
	t.Cleanup(server.Close)
	node, err := exact.NewRPCNode(server.URL + "/")
	require.NoError(t, err)
	_, err = node.GasPrice(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"auth/gasprice"}, rpc.queried())
}

// TestANilNodeSaysSo: a node a failed NewRPCNode left nil reports that instead
// of panicking inside a buyer's request.
func TestANilNodeSaysSo(t *testing.T) {
	var node *exact.RPCNode
	addr := address(signerFrom(t, test1Mnemonic))
	ctx := context.Background()

	_, err := node.NodeChainID(ctx)
	require.Error(t, err)
	_, err = node.GasPrice(ctx)
	require.Error(t, err)
	_, err = node.Account(ctx, addr)
	require.Error(t, err)
	_, err = node.SessionAccount(ctx, addr, addr)
	require.Error(t, err)
}

// TestAnAnswerForNoAccountOrAnotherIsAnError: a node that answers with no
// account, or with another account than the one asked for, would have the
// payment signed over a number and sequence the chain does not hold for the
// payer.
func TestAnAnswerForNoAccountOrAnotherIsAnError(t *testing.T) {
	master := crypto.MustAddressFromString(test1Address)
	session := crypto.MustAddressFromString(payTo)
	accountPath := "auth/accounts/" + test1Address
	sessionPath := accountPath + "/session/" + payTo
	cases := map[string]struct{ path, data string }{
		"a null account":                 {accountPath, `{"BaseAccount": null}`},
		"an empty account":               {accountPath, `{"BaseAccount": {}}`},
		"another account":                {accountPath, strings.Replace(accountJSON, test1Address, payTo, 1)},
		"a null session":                 {sessionPath, `{"BaseSessionAccount": null}`},
		"another session":                {sessionPath, strings.Replace(sessionJSON, `"address": "`+payTo, `"address": "`+test1Address, 1)},
		"a session under another master": {sessionPath, strings.Replace(sessionJSON, `"master_address": "`+test1Address, `"master_address": "`+payTo, 1)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			node := nodeOver(t, &stubRPC{answers: map[string]abci.ResponseQuery{tc.path: answering(tc.data)}})

			var err error
			if tc.path == accountPath {
				_, err = node.Account(context.Background(), master)
			} else {
				_, err = node.SessionAccount(context.Background(), master, session)
			}

			require.Error(t, err)
		})
	}
}

// TestAnAnswerWithNoResultGivesNoVerdict: a node, or a proxy in front of it,
// that answers with no result has not said the account is unfunded, the
// session revoked or the chain unpriced. Reading it as any of those would tell
// the buyer to fund an address, or price the fee at its ceiling.
func TestAnAnswerWithNoResultGivesNoVerdict(t *testing.T) {
	addr := crypto.MustAddressFromString(test1Address)
	for name, raw := range map[string]string{
		"a null result":    `{"jsonrpc": "2.0", "id": "", "result": null}`,
		"no result":        `{"jsonrpc": "2.0", "id": ""}`,
		"a gateway's page": `<html><body>502 Bad Gateway</body></html>`,
	} {
		t.Run(name, func(t *testing.T) {
			node := nodeOver(t, &stubRPC{raw: []byte(raw)})
			ctx := context.Background()

			_, err := node.Account(ctx, addr)
			require.Error(t, err)
			_, unknown := errors.AsType[std.UnknownAddressError](err)
			assert.False(t, unknown, "no answer is not an unfunded account")

			_, err = node.SessionAccount(ctx, addr, addr)
			require.Error(t, err)
			_, notFound := errors.AsType[std.SessionNotFoundError](err)
			assert.False(t, notFound, "no answer is not a revoked session")

			price, err := node.GasPrice(ctx)
			require.Error(t, err)
			assert.Nil(t, price)
		})
	}
}

// TestANodePasswordStaysOutOfErrors: a node reached with credentials in its
// URL fails without printing them, since a read's error reaches logs and, in
// an agent, a model.
func TestANodePasswordStaysOutOfErrors(t *testing.T) {
	server := httptest.NewServer(&stubRPC{rpcErr: errors.New("refused")})
	remote, err := url.Parse(server.URL)
	require.NoError(t, err)
	remote.User = url.UserPassword("buyer", "s3cret")
	node, err := exact.NewRPCNode(remote.String())
	require.NoError(t, err)

	_, err = node.GasPrice(context.Background())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "s3cret", "a refused read")

	server.Close()
	_, err = node.GasPrice(context.Background())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "s3cret", "an unreachable node")
}
