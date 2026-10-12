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
	"sync/atomic"
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

// logged reports whether the seller logged a record with message msg. It fails
// no test, so a condition polled from another goroutine can call it.
func (l *sellerLog) logged(msg string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for line := range bytes.Lines(l.buf.Bytes()) {
		var record struct {
			Msg string `json:"msg"`
		}
		if json.Unmarshal(line, &record) == nil && record.Msg == msg {
			return true
		}
	}
	return false
}

// weatherSeller serves the weather seller for exacttest's offer through the
// facilitator at facilitatorURL, after adjust edits its configuration, and
// returns its URL and what it logs.
func weatherSeller(t *testing.T, facilitatorURL string, adjust ...func(*sellerConfig)) (string, *sellerLog) {
	t.Helper()
	log := &sellerLog{}
	cfg := sellerConfig{
		facilitatorURL: facilitatorURL,
		network:        "gno:" + exacttest.ChainID,
		payTo:          exacttest.PayTo,
		price:          std.MustParseCoin("250000ugnot"),
		log:            slog.New(slog.NewJSONHandler(log, nil)),
	}
	for _, edit := range adjust {
		edit(&cfg)
	}
	handler, err := newSeller(t.Context(), cfg)
	require.NoError(t, err)
	seller := httptest.NewServer(handler)
	t.Cleanup(seller.Close)
	return seller.URL, log
}

// termsOf requests the weather without paying and returns the 402's terms.
func termsOf(t *testing.T, sellerURL string) x402.PaymentRequired {
	t.Helper()

	unpaid, err := http.Get(sellerURL + "/weather")
	require.NoError(t, err)
	require.NoError(t, unpaid.Body.Close())
	require.Equal(t, http.StatusPaymentRequired, unpaid.StatusCode)
	terms, err := base64.StdEncoding.DecodeString(unpaid.Header.Get("PAYMENT-REQUIRED"))
	require.NoError(t, err)
	var required x402.PaymentRequired
	require.NoError(t, json.Unmarshal(terms, &required))
	return required
}

// payWith answers the seller's offer with txB64 as the transaction, and returns
// what the seller answered the paid request with.
func payWith(t *testing.T, sellerURL, txB64 string) *http.Response {
	t.Helper()

	required := termsOf(t, sellerURL)
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

// browserPage opens the priced route the way a browser does and returns the
// response and the page it carries.
func browserPage(t *testing.T, sellerURL string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, sellerURL+"/weather", nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "text/html")
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, string(body)
}

// TestABrowserIsToldWhatTheResourceCostsInGno opens the priced route the way a
// browser does. The middleware answers a browser with an HTML paywall, and its
// built-in one is a wallet app for EVM and Solana chains that renders nothing
// for a gno network.
func TestABrowserIsToldWhatTheResourceCostsInGno(t *testing.T) {
	gnofacilitator := httptest.NewServer(facilitator.New(unreachableChain{}, exacttest.ChainID).Handler())
	t.Cleanup(gnofacilitator.Close)
	sellerURL, _ := weatherSeller(t, gnofacilitator.URL)

	resp, page := browserPage(t, sellerURL)

	require.Equal(t, http.StatusPaymentRequired, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "text/html")
	assert.NotEmpty(t, resp.Header.Get("PAYMENT-REQUIRED"), "the terms still travel in the header")

	assert.NotContains(t, page, "window.x402", "the built-in EVM/Solana paywall was served")
	assert.Contains(t, page, ">0.25<", "the amount a person reads")
	assert.Contains(t, page, "GNOT")
	assert.Contains(t, page, "250000ugnot")
	assert.Contains(t, page, "gno:dev")
	assert.Contains(t, page, "g1u7y667z64x2h7vc6fmpcprgey4ck233jaww9zq")
	assert.Contains(t, page, "buy.mjs", "the page names a buyer that can pay it without a wallet")
	assert.Contains(t, page, "Pay with Adena")
	assert.Contains(t, page, "Testnet only")
}

// The page pays through the script the seller inlines, so it must arrive
// intact: html/template escapes a string it does not know to be JavaScript.
func TestTheBrowserPageCarriesItsPaymentScriptIntact(t *testing.T) {
	gnofacilitator := httptest.NewServer(facilitator.New(unreachableChain{}, exacttest.ChainID).Handler())
	t.Cleanup(gnofacilitator.Close)
	sellerURL, _ := weatherSeller(t, gnofacilitator.URL)

	_, page := browserPage(t, sellerURL)

	assert.Contains(t, page, paywallScript)
}

// The page makes no request beyond the seller: no script, style, font or image
// from anywhere else. Links are navigation, not requests.
func TestTheBrowserPageIsSelfContained(t *testing.T) {
	gnofacilitator := httptest.NewServer(facilitator.New(unreachableChain{}, exacttest.ChainID).Handler())
	t.Cleanup(gnofacilitator.Close)
	sellerURL, _ := weatherSeller(t, gnofacilitator.URL)

	_, page := browserPage(t, sellerURL)

	for _, fetches := range []string{" src=", "<link", "<iframe", "@import", "url("} {
		assert.NotContains(t, page, fetches)
	}
	t.Logf("page: %d bytes", len(page))
}

func TestTheAmountIsShownInGNOT(t *testing.T) {
	for coin, want := range map[string][2]string{
		"250000ugnot":  {"0.25", "GNOT"},
		"1000000ugnot": {"1", "GNOT"},
		"1500000ugnot": {"1.5", "GNOT"},
		"1ugnot":       {"0.000001", "GNOT"},
		// GNOT is a name for ugnot only; another denomination is shown as is.
		"500atom": {"500", "atom"},
	} {
		t.Run(coin, func(t *testing.T) {
			amount, unit := shownAmount(std.MustParseCoin(coin))
			assert.Equal(t, want, [2]string{amount, unit})
		})
	}
}

// Someone who types the host alone lands on the demo. The redirect is relative,
// so it also holds behind a proxy that serves the seller under a path.
func TestTheRootRedirectsToTheWeather(t *testing.T) {
	gnofacilitator := httptest.NewServer(facilitator.New(unreachableChain{}, exacttest.ChainID).Handler())
	t.Cleanup(gnofacilitator.Close)
	sellerURL, _ := weatherSeller(t, gnofacilitator.URL)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	resp, err := client.Get(sellerURL + "/")
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	assert.Equal(t, http.StatusSeeOther, resp.StatusCode)
	assert.Equal(t, "weather", resp.Header.Get("Location"))
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

// TestThe402NamesThePublicURL serves the seller on a loopback address, as it
// runs behind a TLS proxy. The 402 must name the URL buyers reach, or a client
// that checks it pays for a resource at an address it never asked for.
func TestThe402NamesThePublicURL(t *testing.T) {
	gnofacilitator := httptest.NewServer(facilitator.New(unreachableChain{}, exacttest.ChainID).Handler())
	t.Cleanup(gnofacilitator.Close)
	sellerURL, _ := weatherSeller(t, gnofacilitator.URL, func(cfg *sellerConfig) {
		cfg.publicURL = "https://weather.example"
	})

	required := termsOf(t, sellerURL)

	require.NotNil(t, required.Resource)
	assert.Equal(t, "https://weather.example/weather", required.Resource.URL)
}

func TestWithoutAPublicURLThe402NamesTheAddressItWasReachedAt(t *testing.T) {
	gnofacilitator := httptest.NewServer(facilitator.New(unreachableChain{}, exacttest.ChainID).Handler())
	t.Cleanup(gnofacilitator.Close)
	sellerURL, _ := weatherSeller(t, gnofacilitator.URL)

	required := termsOf(t, sellerURL)

	require.NotNil(t, required.Resource)
	assert.Equal(t, sellerURL+"/weather", required.Resource.URL)
}

// A public URL is what every 402 names, so one that is not a plain absolute
// http(s) URL is refused at startup rather than advertised to every buyer: a
// credential in it would go out in every 402.
func TestASellerRefusesAPublicURLBuyersCannotReach(t *testing.T) {
	for _, publicURL := range []string{
		"weather.example", "ftp://weather.example", "https://", "https://weather.example?x=1",
		"https://weather.example?", "https://user:secret@weather.example",
	} {
		t.Run(publicURL, func(t *testing.T) {
			_, err := newSeller(t.Context(), sellerConfig{
				facilitatorURL: "http://127.0.0.1:1",
				network:        "gno:" + exacttest.ChainID,
				payTo:          exacttest.PayTo,
				price:          std.MustParseCoin("250000ugnot"),
				log:            slog.New(slog.DiscardHandler),
				publicURL:      publicURL,
			})
			require.ErrorContains(t, err, "-public-url")
		})
	}
}

// TestThePricedRouteIsRateLimitedBeforeThePaymentIsChecked spends the route's
// only token on an unpaid request. The paid request after it gets a 429, and the
// facilitator never hears of it: every paid request costs the facilitator a
// verify and a settle, from this seller's one address.
func TestThePricedRouteIsRateLimitedBeforeThePaymentIsChecked(t *testing.T) {
	var verifies atomic.Int32
	gno := facilitator.New(unreachableChain{}, exacttest.ChainID).Handler()
	gnofacilitator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/verify" {
			verifies.Add(1)
		}
		gno.ServeHTTP(w, r)
	}))
	t.Cleanup(gnofacilitator.Close)
	sellerURL, _ := weatherSeller(t, gnofacilitator.URL, func(cfg *sellerConfig) {
		cfg.ratePerSecond = 0.001
		cfg.rateBurst = 1
	})

	paid := payWith(t, sellerURL, exacttest.PlaceholderTx(t, nil))

	assert.Equal(t, http.StatusTooManyRequests, paid.StatusCode)
	assert.Zero(t, verifies.Load(), "the payment reached the facilitator")
}

// A write timeout under the time a paid request may take would cut the
// connection mid-settle: the buyer is charged and never gets the response.
func TestTheServerOutlastsAPaidRequest(t *testing.T) {
	srv := newServer(":0", http.NotFoundHandler())

	assert.Greater(t, srv.WriteTimeout, paymentTimeout)
	assert.Positive(t, srv.IdleTimeout, "an idle keep-alive connection is held forever")
	assert.Positive(t, srv.ReadTimeout, "a request body is read without a deadline")
}
