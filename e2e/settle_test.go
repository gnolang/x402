package e2e

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gnolang/gno/gno.land/pkg/gnoclient"
	"github.com/gnolang/gno/gno.land/pkg/gnoland"
	"github.com/gnolang/gno/gno.land/pkg/integration"
	"github.com/gnolang/gno/gnovm/pkg/gnoenv"
	"github.com/gnolang/gno/tm2/pkg/amino"
	"github.com/gnolang/gno/tm2/pkg/bft/mempool"
	"github.com/gnolang/gno/tm2/pkg/bft/node"
	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
	"github.com/gnolang/gno/tm2/pkg/crypto"
	"github.com/gnolang/gno/tm2/pkg/crypto/ed25519"
	"github.com/gnolang/gno/tm2/pkg/log"
	p2pconfig "github.com/gnolang/gno/tm2/pkg/p2p/config"
	p2pTypes "github.com/gnolang/gno/tm2/pkg/p2p/types"
	"github.com/gnolang/gno/tm2/pkg/sdk/bank"
	"github.com/gnolang/gno/tm2/pkg/std"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	x402 "github.com/x402-foundation/x402/go/v2"
	"github.com/x402-foundation/x402/go/v2/types"

	"github.com/gnoverse/x402/facilitator"
	"github.com/gnoverse/x402/facilitator/exact"
)

// TestDuplicateSettle_OneNodeSettlesOnce hands one payment to two facilitators
// that broadcast through the same node, at the same instant.
//
// Exactly one reports success. Arriving together, both read the payer's account
// before either broadcast commits, so both pass every check and broadcast the
// same bytes. The node admits the first into its mempool and refuses the second
// with ErrTxInCache before running it. That refusal is not an abci.Error, so the
// second facilitator answers 503 with no verdict rather than a settlement. A
// node's mempool is therefore an atomic deduplication point for every
// facilitator broadcasting through it.
func TestDuplicateSettle_OneNodeSettlesOnce(t *testing.T) {
	logs := captureLogs(t)
	payer, payee := payerKey(t), payeeAddress()
	validator := startValidator(t, payee)
	before := readAccounts(t, validator, payer, payee)

	answers := settleConcurrently(t, signedSettleRequest(t, validator, payer, payee),
		startFacilitator(t, exact.NewGnoclientNode(validator.client)),
		startFacilitator(t, exact.NewGnoclientNode(validator.client)))

	require.Equal(t, 1, settledCount(answers), "answers: %v", answers)
	for _, answer := range answers {
		if !answer.settled() {
			assert.Equal(t, http.StatusServiceUnavailable, answer.status, "answers: %v", answers)
		}
	}
	noVerdict := logRecords(t, logs, "x402 settle: chain unreachable, reporting no verdict")
	require.Len(t, noVerdict, 1, "captured:\n%s", logs)
	assert.Contains(t, noVerdict[0]["err"], mempool.ErrTxInCache.Error())

	after := readAccounts(t, validator, payer, payee)
	assert.Equal(t, before.payeeUgnot+dupPrice, after.payeeUgnot, "the payee is paid once")
	assert.Equal(t, before.payerSequence+1, after.payerSequence, "one transaction committed")
}

// TestDuplicateSettle_TwoNodesBothReportSuccess hands one payment to two
// facilitators, each broadcasting through its own node of one chain, at the same
// instant.
//
// Both report success:true while the payee is paid once: one payment buys two
// resources. x402's exact scheme forbids this. A mechanism whose resubmitted
// payment is indistinguishable from the original must deduplicate settlements
// atomically across every process serving /settle
// (specs/schemes/exact/scheme_exact.md, Duplicate delivery), and a facilitator
// deduplicates within its own process only. These two share nothing.
//
// Each node admits the transaction into its own mempool, because neither has
// heard of it from the other yet: tm2 gossips on a flush interval. The validator
// commits one copy, and each facilitator finds that delivery by looking up the
// hash on its own node, because the bytes and so the hash are the same. Neither
// answer says whose broadcast was included.
//
// This asserts the limit as it stands. Deduplicating across processes makes it
// fail, and it should then assert exactly one success.
func TestDuplicateSettle_TwoNodesBothReportSuccess(t *testing.T) {
	payer, payee := payerKey(t), payeeAddress()
	validator := startValidator(t, payee)
	follower := startFollower(t, validator)
	before := readAccounts(t, validator, payer, payee)

	answers := settleConcurrently(t, signedSettleRequest(t, validator, payer, payee),
		startFacilitator(t, exact.NewGnoclientNode(validator.client)),
		startFacilitator(t, exact.NewGnoclientNode(follower.client)))

	require.Equal(t, 2, settledCount(answers), "answers: %v", answers)

	after := readAccounts(t, validator, payer, payee)
	assert.Equal(t, before.payeeUgnot+dupPrice, after.payeeUgnot, "the payee is paid once")
	assert.Equal(t, before.payerSequence+1, after.payerSequence, "one transaction committed")
}

// TestDuplicateSettle_OneFacilitatorBehindABalancerSettlesOnce hands one payment
// to a single facilitator twice at the same instant, through an endpoint that
// sends each broadcast to the next of two nodes: the shape of a load-balanced
// public RPC, which answered forty requests from eight distinct nodes on onyx-1.
//
// Two nodes would each admit a copy and answer both broadcasts with the one
// delivery, as two facilitators do. One facilitator settles the payment once and
// refuses the copy as a duplicate.
func TestDuplicateSettle_OneFacilitatorBehindABalancerSettlesOnce(t *testing.T) {
	payer, payee := payerKey(t), payeeAddress()
	validator := startValidator(t, payee)
	follower := startFollower(t, validator)
	before := readAccounts(t, validator, payer, payee)
	balancer := &broadcastBalancer{Node: exact.NewGnoclientNode(validator.client), nodes: []exact.Node{
		exact.NewGnoclientNode(validator.client), exact.NewGnoclientNode(follower.client),
	}}
	facilitatorURL := startFacilitator(t, balancer)

	answers := settleConcurrently(t, signedSettleRequest(t, validator, payer, payee), facilitatorURL, facilitatorURL)

	require.Equal(t, 1, settledCount(answers), "answers: %v", answers)
	for _, answer := range answers {
		if !answer.settled() {
			assert.Equal(t, exact.ReasonDuplicateSettlement, answer.response.ErrorReason, "answers: %v", answers)
		}
	}
	after := readAccounts(t, validator, payer, payee)
	assert.Equal(t, before.payeeUgnot+dupPrice, after.payeeUgnot, "the payee is paid once")
	assert.Equal(t, before.payerSequence+1, after.payerSequence, "one transaction committed")
}

// broadcastBalancer reads the chain through its embedded Node and sends each
// broadcast to the next of its nodes in turn.
type broadcastBalancer struct {
	exact.Node
	nodes []exact.Node
	next  atomic.Uint32
}

func (b *broadcastBalancer) Broadcast(tx []byte) (string, error) {
	i := b.next.Add(1) - 1
	return b.nodes[int(i)%len(b.nodes)].Broadcast(tx)
}

// TestAReplayedPaymentIsRefused settles a payment, then replays the same /settle
// body to the facilitator that settled it and to another one.
//
// The facilitator that settled it refuses the replay as a duplicate before
// reading the chain. Another facilitator remembers nothing, and is refused by
// the chain itself: x402's exact scheme requires a consumed payment to fail at
// settle. That refusal is a sequence mismatch rather than an invalid signature:
// the signature still verifies over the sequence it was signed at, which is what
// tells a spent payment from a forged one. Only a chain that ran the ante can
// show it, since the unit tests read a scripted account.
func TestAReplayedPaymentIsRefused(t *testing.T) {
	payer, payee := payerKey(t), payeeAddress()
	validator := startValidator(t, payee)
	settledBy := startFacilitator(t, exact.NewGnoclientNode(validator.client))
	body := signedSettleRequest(t, validator, payer, payee)

	first, err := postSettle(settledBy, body)
	require.NoError(t, err)
	require.True(t, first.settled(), "first: %v", first)
	paid := readAccounts(t, validator, payer, payee)

	again, err := postSettle(settledBy, body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, again.status, "again: %v", again)
	assert.False(t, again.response.Success)
	assert.Equal(t, exact.ReasonDuplicateSettlement, again.response.ErrorReason)
	assert.Equal(t, first.response.Transaction, again.response.Transaction,
		"the refusal carries the hash the payment settled under")

	elsewhere, err := postSettle(startFacilitator(t, exact.NewGnoclientNode(validator.client)), body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, elsewhere.status, "elsewhere: %v", elsewhere)
	assert.False(t, elsewhere.response.Success)
	assert.Equal(t, exact.ReasonSequenceMismatch, elsewhere.response.ErrorReason)
	assert.Equal(t, payer.PubKey().Address().String(), elsewhere.response.Payer,
		"the signature verified, so the refusal names the payer")

	assert.Equal(t, paid, readAccounts(t, validator, payer, payee), "neither replay moved anything")
}

// ---- Chain

// dupPrice is what every payment here sends, in ugnot.
const dupPrice = 250_000

// payeeStart is the payee's genesis balance. Funding it at genesis means its
// account exists before the first payment, so a balance read never has to tell
// "no account" from zero.
const payeeStart = 1_000_000_000

// chainNode is one in-process node of the test chain and a client for its RPC.
type chainNode struct {
	node   *node.Node
	client *gnoclient.Client
}

// startValidator starts the chain's only validator, with the payee funded at
// genesis. It alone produces blocks.
func startValidator(t *testing.T, payee crypto.Address) *chainNode {
	t.Helper()

	cfg := integration.TestingMinimalNodeConfig(gnoenv.RootDir())
	genesis := cfg.Genesis.AppState.(gnoland.GnoGenesisState)
	genesis.Balances = append(genesis.Balances, gnoland.Balance{
		Address: payee,
		Amount:  std.NewCoins(std.NewCoin("ugnot", payeeStart)),
	})
	cfg.Genesis.AppState = genesis
	return startNode(t, cfg)
}

// startFollower starts a full node of the validator's chain, peered to it over
// loopback. It holds no voting power, so it commits the blocks the validator
// produces, and it relays its mempool to the validator as any node does.
func startFollower(t *testing.T, validator *chainNode) *chainNode {
	t.Helper()

	cfg := integration.TestingMinimalNodeConfig(validator.node.Config().RootDir)
	cfg.Genesis = validator.node.GenesisDoc()
	cfg.TMConfig.P2P.PersistentPeers = p2pTypes.NetAddressString(
		validator.node.NodeInfo().ID(), validator.node.Config().P2P.ListenAddress)
	follower := startNode(t, cfg)

	require.Eventually(t, func() bool {
		return validator.node.Switch().Peers().NumInbound() == 1 &&
			follower.node.BlockStore().Height() >= validator.node.BlockStore().Height()
	}, 30*time.Second, 50*time.Millisecond, "the follower never peered with the validator and caught up")
	return follower
}

// startNode starts an in-memory node.
//
// It gossips at tm2's default flush interval rather than the test config's
// shorter one, because that is what a deployed node runs, and it bounds how long
// a second submission has to reach its own node before the first one's gossip
// does.
//
// Its peer address book lives in the test's own directory, since GNOROOT is the
// read-only module cache and two nodes must not share one file. Its standard
// library is loaded uncached: gno caches it once per process and panics when a
// later node names another GNOROOT, and the scenarios in
// TestAStockClientPaysAGnoSeller start theirs from a mirror of their own.
func startNode(t *testing.T, cfg *gnoland.InMemoryNodeConfig) *chainNode {
	t.Helper()

	cfg.TMConfig.P2P.FlushThrottleTimeout = p2pconfig.DefaultP2PConfig().FlushThrottleTimeout
	cfg.TMConfig.P2P.AddrBook = filepath.Join(t.TempDir(), "addrbook.json")
	cfg.CacheStdlibLoad = false
	n, rpcAddr := integration.TestingInMemoryNode(t, log.NewNoopLogger(), cfg)
	t.Cleanup(func() { assert.NoError(t, n.Stop()) })

	rpc, err := rpcclient.NewHTTPClient(httpURL(rpcAddr))
	require.NoError(t, err)
	return &chainNode{node: n, client: &gnoclient.Client{RPCClient: rpc}}
}

// startFacilitator serves a facilitator that reads and broadcasts through node.
func startFacilitator(t *testing.T, node exact.Node) string {
	t.Helper()

	f := facilitator.New(node, chainID)
	server := httptest.NewServer(f.Handler())
	t.Cleanup(server.Close)
	return server.URL
}

// accounts is what a payment changes: the payee's balance, and the payer's
// sequence, which moves once per transaction the chain commits.
type accounts struct {
	payeeUgnot    int64
	payerSequence uint64
}

func readAccounts(t *testing.T, at *chainNode, payer crypto.PrivKey, payee crypto.Address) accounts {
	t.Helper()

	payeeAcc, _, err := at.client.QueryAccount(payee)
	require.NoError(t, err)
	payerAcc, _, err := at.client.QueryAccount(payer.PubKey().Address())
	require.NoError(t, err)
	return accounts{payeeUgnot: payeeAcc.Coins.AmountOf("ugnot"), payerSequence: payerAcc.Sequence}
}

// ---- Payment

// payerKey is test1's key, the account the in-memory genesis funds.
func payerKey(t *testing.T) crypto.PrivKey {
	t.Helper()

	key, err := integration.GeneratePrivKeyFromMnemonic(integration.DefaultAccount_Seed, "", 0, 0)
	require.NoError(t, err)
	return key
}

// payeeAddress is a fixed address that belongs to nobody else in the test.
func payeeAddress() crypto.Address {
	return ed25519.GenPrivKeyFromSecret([]byte("x402-duplicate-settle-payee")).PubKey().Address()
}

// settleRequest is the body a seller posts to /settle.
type settleRequest struct {
	X402Version         int                       `json:"x402Version"`
	PaymentPayload      types.PaymentPayload      `json:"paymentPayload"`
	PaymentRequirements types.PaymentRequirements `json:"paymentRequirements"`
}

// signedSettleRequest is a /settle body for a fresh payment of dupPrice from the
// payer to the payee, signed at the payer's current sequence. It is what a seller
// forwards after reading a buyer's PAYMENT-SIGNATURE, so two sellers handed the
// same header forward the same body.
func signedSettleRequest(t *testing.T, at *chainNode, payer crypto.PrivKey, payee crypto.Address) []byte {
	t.Helper()

	acc, _, err := at.client.QueryAccount(payer.PubKey().Address())
	require.NoError(t, err)

	// The fee is the JS buyer's, so the transaction has the shape a buyer signs.
	tx := std.Tx{
		Msgs: []std.Msg{bank.MsgSend{
			FromAddress: payer.PubKey().Address(),
			ToAddress:   payee,
			Amount:      std.NewCoins(std.NewCoin("ugnot", dupPrice)),
		}},
		Fee: std.NewFee(10_000_000, std.MustParseCoin("1000000ugnot")),
	}
	signBytes, err := tx.GetSignBytes(chainID, acc.AccountNumber, acc.Sequence)
	require.NoError(t, err)
	signature, err := payer.Sign(signBytes)
	require.NoError(t, err)
	tx.Signatures = []std.Signature{{PubKey: payer.PubKey(), Signature: signature}}
	raw, err := amino.Marshal(tx)
	require.NoError(t, err)

	requirements := types.PaymentRequirements{
		Scheme:            "exact",
		Network:           network,
		Amount:            strconv.Itoa(dupPrice),
		Asset:             "ugnot",
		PayTo:             payee.String(),
		MaxTimeoutSeconds: 60,
	}
	body, err := json.Marshal(settleRequest{
		X402Version:         2,
		PaymentRequirements: requirements,
		PaymentPayload: types.PaymentPayload{
			X402Version: 2,
			Accepted:    requirements,
			Payload:     map[string]any{"transaction": base64.StdEncoding.EncodeToString(raw)},
		},
	})
	require.NoError(t, err)
	return body
}

// ---- Settle

// settleAnswer is how a facilitator answered one /settle request. response is
// decoded from a 200 only: any other status carries no settlement.
type settleAnswer struct {
	status   int
	response x402.SettleResponse
}

func (a settleAnswer) settled() bool { return a.status == http.StatusOK && a.response.Success }

func (a settleAnswer) String() string {
	switch {
	case a.status != http.StatusOK:
		return fmt.Sprintf("HTTP %d", a.status)
	case a.response.Success:
		return "success:true tx " + a.response.Transaction
	default:
		return "success:false " + a.response.ErrorReason
	}
}

func settledCount(answers []settleAnswer) int {
	n := 0
	for _, a := range answers {
		if a.settled() {
			n++
		}
	}
	return n
}

// postSettle sends one /settle request. It returns its error rather than failing
// the test, because it runs on a goroutine the test cannot be stopped from.
func postSettle(facilitatorURL string, body []byte) (settleAnswer, error) {
	resp, err := http.Post(facilitatorURL+"/settle", "application/json", bytes.NewReader(body))
	if err != nil {
		return settleAnswer{}, err
	}
	defer resp.Body.Close()

	answer := settleAnswer{status: resp.StatusCode}
	if resp.StatusCode != http.StatusOK {
		_, err := io.Copy(io.Discard, resp.Body)
		return answer, err
	}
	err = json.NewDecoder(resp.Body).Decode(&answer.response)
	return answer, err
}

// settleConcurrently sends the same /settle body to every facilitator at once,
// and returns their answers in the same order.
func settleConcurrently(t *testing.T, body []byte, facilitatorURLs ...string) []settleAnswer {
	t.Helper()

	answers := make([]settleAnswer, len(facilitatorURLs))
	errs := make([]error, len(facilitatorURLs))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, url := range facilitatorURLs {
		wg.Go(func() {
			<-start
			answers[i], errs[i] = postSettle(url, body)
		})
	}
	close(start)
	wg.Wait()
	require.NoError(t, errors.Join(errs...))
	return answers
}

// ---- Logs

// captureLogs routes the default logger, which the facilitator logs through,
// into a buffer for the rest of the test. It swaps a process-wide logger, so a
// test using it must not run in parallel.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

// logRecords returns every captured record carrying the given message.
func logRecords(t *testing.T, buf *bytes.Buffer, msg string) []map[string]any {
	t.Helper()

	var records []map[string]any
	decoder := json.NewDecoder(bytes.NewReader(buf.Bytes()))
	for {
		var record map[string]any
		err := decoder.Decode(&record)
		if errors.Is(err, io.EOF) {
			return records
		}
		require.NoError(t, err, "captured logs are not a JSON stream:\n%s", buf)
		if record["msg"] == msg {
			records = append(records, record)
		}
	}
}
