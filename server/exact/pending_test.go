package exact_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	x402 "github.com/x402-foundation/x402/go/v2"
	x402http "github.com/x402-foundation/x402/go/v2/http"
	nethttpmw "github.com/x402-foundation/x402/go/v2/http/nethttp"
	"github.com/x402-foundation/x402/go/v2/types"

	"github.com/gnolang/x402/facilitator"
	"github.com/gnolang/x402/facilitator/exact/exacttest"
	gnoexact "github.com/gnolang/x402/server/exact"
)

// pendingAnswerWithin is the binary's answer deadline, scaled down so a settle
// waits out its delivery for half a second: this less the one-second margin.
const pendingAnswerWithin = 1500 * time.Millisecond

// sellerOverChain runs the canonical middleware for exacttest's offer against a
// gno facilitator over node, behind the answer deadline the binary serves with.
func sellerOverChain(t *testing.T, node *exacttest.Node) string {
	t.Helper()

	gnofacilitator := httptest.NewServer(http.TimeoutHandler(
		facilitator.New(node, exacttest.ChainID).Handler(), pendingAnswerWithin, "the chain did not answer in time"))
	t.Cleanup(gnofacilitator.Close)

	network := x402.Network("gno:" + exacttest.ChainID)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /weather", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"forecast":"sunny"}`))
	})
	seller := httptest.NewServer(nethttpmw.X402Payment(nethttpmw.Config{
		Routes: x402http.RoutesConfig{
			"GET /weather": {
				Accepts: x402http.PaymentOptions{{
					Scheme:  "exact",
					PayTo:   exacttest.PayTo,
					Price:   x402.AssetAmount{Asset: "ugnot", Amount: "250000"},
					Network: network,
				}},
			},
		},
		Facilitator: x402http.NewHTTPFacilitatorClient(&x402http.FacilitatorConfig{URL: gnofacilitator.URL}),
		Schemes: []nethttpmw.SchemeConfig{
			{Network: network, Server: gnoexact.NewExactGnoScheme()},
		},
		SyncFacilitatorOnStart: true,
	})(mux))
	t.Cleanup(seller.Close)
	return seller.URL
}

// payWith pays the seller's offer with txB64, a transaction signed for exactly
// that offer, and returns what the seller answered the paid request with.
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

func settlement(t *testing.T, paid *http.Response) x402.SettleResponse {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(paid.Header.Get("PAYMENT-RESPONSE"))
	require.NoError(t, err, "the seller answers a settle with PAYMENT-RESPONSE")
	var settled x402.SettleResponse
	require.NoError(t, json.Unmarshal(raw, &settled))
	return settled
}

// TestASellerServesAPaymentThatCommitsAfterItsFirstSettle is what a stock
// seller does with a gno payment no block had executed when its settle answered:
// it settles once more, the facilitator asks the chain about the transaction it
// already broadcast, and the buyer is served. The payment is broadcast once.
func TestASellerServesAPaymentThatCommitsAfterItsFirstSettle(t *testing.T) {
	txB64, account := exacttest.SignedTx(t, nil)
	var awaits, broadcasts atomic.Int32
	node := &exacttest.Node{
		Account:     account,
		Hash:        "abc123",
		OnBroadcast: func() { broadcasts.Add(1) },
		Delivery: func(ctx context.Context) error {
			if awaits.Add(1) == 1 {
				return exacttest.NeverDelivered(ctx)
			}
			return nil
		},
	}

	paid := payWith(t, sellerOverChain(t, node), txB64)

	assert.Equal(t, http.StatusOK, paid.StatusCode)
	settled := settlement(t, paid)
	assert.True(t, settled.Success)
	assert.Equal(t, "abc123", settled.Transaction)
	assert.Equal(t, int32(1), broadcasts.Load(), "the second settle reconciles rather than broadcasting again")
	assert.Equal(t, int32(2), awaits.Load())
}

// TestASellerTellsTheBuyerAPaymentIsStillPending is the other end: no block has
// executed the payment by the seller's second settle either. The buyer is not
// served, and is told the payment is pending together with its hash, which is
// how it finds out on chain whether it was charged.
func TestASellerTellsTheBuyerAPaymentIsStillPending(t *testing.T) {
	txB64, account := exacttest.SignedTx(t, nil)
	node := &exacttest.Node{Account: account, Hash: "abc123", Delivery: exacttest.NeverDelivered}

	paid := payWith(t, sellerOverChain(t, node), txB64)

	assert.Equal(t, http.StatusPaymentRequired, paid.StatusCode)
	settled := settlement(t, paid)
	assert.False(t, settled.Success)
	assert.Equal(t, x402.ErrSettlementPending, settled.ErrorReason)
	assert.Equal(t, "abc123", settled.Transaction)
}
