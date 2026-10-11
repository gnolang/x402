package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gnolang/gno/tm2/pkg/std"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gnoverse/x402/facilitator"
	"github.com/gnoverse/x402/facilitator/exact"
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

// TestABrowserIsToldWhatTheResourceCostsInGno opens the priced route the way a
// browser does. The middleware answers a browser with an HTML paywall, and its
// built-in one is a wallet app for EVM and Solana chains that renders nothing
// for a gno network.
func TestABrowserIsToldWhatTheResourceCostsInGno(t *testing.T) {
	gnofacilitator := httptest.NewServer(facilitator.New(unreachableChain{}, "dev").Handler())
	t.Cleanup(gnofacilitator.Close)

	handler, err := newSeller(sellerConfig{
		facilitatorURL: gnofacilitator.URL,
		network:        "gno:dev",
		payTo:          "g1u7y667z64x2h7vc6fmpcprgey4ck233jaww9zq",
		price:          std.MustParseCoin("250000ugnot"),
	})
	require.NoError(t, err)
	seller := httptest.NewServer(handler)
	t.Cleanup(seller.Close)

	req, err := http.NewRequest(http.MethodGet, seller.URL+"/weather", nil)
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
