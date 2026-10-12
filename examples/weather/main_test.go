package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gnolang/gno/tm2/pkg/std"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	x402 "github.com/x402-foundation/x402/go/v2"
	"github.com/x402-foundation/x402/go/v2/types"

	"github.com/gnoverse/x402/facilitator"
	"github.com/gnoverse/x402/facilitator/exact"
	"github.com/gnoverse/x402/facilitator/exact/exacttest"
)

// unreachableChain stands in for the node. A 402 is decided from the seller's
// own configuration, so a call here means an unpaid request reached the chain.
type unreachableChain struct{}

func (unreachableChain) SignerAccount(context.Context, *std.Tx) (exact.SignerAccount, error) {
	panic("an unpaid request must be answered without reading an account")
}

func (unreachableChain) Simulate(*std.Tx) error {
	panic("an unpaid request must be answered without simulating")
}

func (unreachableChain) Broadcast([]byte) (string, error) {
	panic("an unpaid request must be answered without broadcasting")
}

func (unreachableChain) AwaitDelivery(context.Context, string) error {
	panic("an unpaid request must be answered without waiting for a delivery")
}

// sellerLog is what the seller logs, as JSON lines. The seller writes from its
// own goroutines, so writes and reads share a lock.
type sellerLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *sellerLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

// recordsWith returns the logged records that carry key.
func (l *sellerLog) recordsWith(t *testing.T, key string) []map[string]any {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	var found []map[string]any
	for line := range bytes.Lines(l.buf.Bytes()) {
		var record map[string]any
		require.NoError(t, json.Unmarshal(line, &record))
		if _, ok := record[key]; ok {
			found = append(found, record)
		}
	}
	return found
}

// weatherSeller serves the weather seller for exacttest's offer through the
// facilitator at facilitatorURL, and returns its URL and what it logs.
func weatherSeller(t *testing.T, facilitatorURL string) (string, *sellerLog) {
	t.Helper()
	log := &sellerLog{}
	handler, err := newSeller(sellerConfig{
		facilitatorURL: facilitatorURL,
		network:        "gno:" + exacttest.ChainID,
		payTo:          exacttest.PayTo,
		price:          std.MustParseCoin("250000ugnot"),
		log:            slog.New(slog.NewJSONHandler(log, nil)),
	})
	require.NoError(t, err)
	seller := httptest.NewServer(handler)
	t.Cleanup(seller.Close)
	return seller.URL, log
}

// payWith answers the seller's offer with txB64 as the transaction, and returns
// what the seller answered the paid request with.
func payWith(t *testing.T, sellerURL, txB64 string) *http.Response {
	t.Helper()

	unpaid, err := http.Get(sellerURL + "/weather")
	require.NoError(t, err)
	require.NoError(t, unpaid.Body.Close())
	require.Equal(t, http.StatusPaymentRequired, unpaid.StatusCode)
	terms, err := base64.StdEncoding.DecodeString(unpaid.Header.Get("PAYMENT-REQUIRED"))
	require.NoError(t, err)
	var required x402.PaymentRequired
	require.NoError(t, json.Unmarshal(terms, &required))
	require.Len(t, required.Accepts, 1)

	payload, err := json.Marshal(types.PaymentPayload{
		X402Version: 2,
		Accepted:    required.Accepts[0],
		Payload:     map[string]any{"transaction": txB64},
	})
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, sellerURL+"/weather", nil)
	require.NoError(t, err)
	req.Header.Set("PAYMENT-SIGNATURE", base64.StdEncoding.EncodeToString(payload))

	paid, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = paid.Body.Close() })
	return paid
}

// TestABrowserIsToldWhatTheResourceCostsInGno opens the priced route the way a
// browser does. The middleware answers a browser with an HTML paywall, and its
// built-in one is a wallet app for EVM and Solana chains that renders nothing
// for a gno network.
func TestABrowserIsToldWhatTheResourceCostsInGno(t *testing.T) {
	gnofacilitator := httptest.NewServer(facilitator.New(unreachableChain{}, exacttest.ChainID).Handler())
	t.Cleanup(gnofacilitator.Close)
	sellerURL, _ := weatherSeller(t, gnofacilitator.URL)

	req, err := http.NewRequest(http.MethodGet, sellerURL+"/weather", nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "text/html")
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusPaymentRequired, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "text/html")
	assert.NotEmpty(t, resp.Header.Get("PAYMENT-REQUIRED"), "the terms still travel in the header")

	page := string(body)
	assert.NotContains(t, page, "window.x402", "the built-in EVM/Solana paywall was served")
	assert.Contains(t, page, "250000ugnot")
	assert.Contains(t, page, "gno:dev")
	assert.Contains(t, page, "g1u7y667z64x2h7vc6fmpcprgey4ck233jaww9zq")
	assert.Contains(t, page, "buy.mjs", "the page names a buyer that can pay it")
}

// TestASellerLogsWhyAPaymentWasRefused pays from an account that cannot cover
// the payment. The buyer gets the same 402 as a request that paid nothing, so
// the seller's log is the only place the refusal shows.
func TestASellerLogsWhyAPaymentWasRefused(t *testing.T) {
	txB64, account := exacttest.SignedTx(t, nil)
	node := &exacttest.Node{Account: account, SimulateErr: std.ErrInsufficientFunds("insufficient funds")}
	gnofacilitator := httptest.NewServer(facilitator.New(node, exacttest.ChainID).Handler())
	t.Cleanup(gnofacilitator.Close)
	sellerURL, log := weatherSeller(t, gnofacilitator.URL)

	paid := payWith(t, sellerURL, txB64)

	require.Equal(t, http.StatusPaymentRequired, paid.StatusCode, "the payment was not refused")
	refusals := log.recordsWith(t, "reason")
	require.Len(t, refusals, 1)
	assert.Equal(t, "invalid_exact_gno_transaction_simulation_failed", refusals[0]["reason"])
	assert.Equal(t, exacttest.Payer, refusals[0]["payer"], "the signature verified, so the payer is known")
}

// TestASellerDoesNotLogAFacilitatorOutageAsARefusal stops the facilitator after
// the seller has learned what it supports. Nobody checked the payment, so the
// seller logs the facilitator's failure and names no reason against the payment.
func TestASellerDoesNotLogAFacilitatorOutageAsARefusal(t *testing.T) {
	gnofacilitator := httptest.NewServer(facilitator.New(unreachableChain{}, exacttest.ChainID).Handler())
	sellerURL, log := weatherSeller(t, gnofacilitator.URL)
	gnofacilitator.Close()

	paid := payWith(t, sellerURL, exacttest.PlaceholderTx(t, nil))

	assert.Equal(t, http.StatusPaymentRequired, paid.StatusCode)
	assert.Empty(t, log.recordsWith(t, "reason"))
	assert.Len(t, log.recordsWith(t, "err"), 1, "the seller is told the facilitator failed")
}
