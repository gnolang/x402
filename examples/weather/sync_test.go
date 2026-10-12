package main

import (
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gnolang/gno/tm2/pkg/std"
	"github.com/stretchr/testify/require"

	"github.com/gnoverse/x402/facilitator"
	"github.com/gnoverse/x402/facilitator/exact/exacttest"
)

// TestASellerStartedBeforeItsFacilitatorTakesPaymentsOnceItIsUp starts the
// seller while nothing listens at the facilitator's address, then starts the
// facilitator there. The startup sync is what tells the middleware which
// facilitator serves the network, and upstream makes it once: without a retry,
// the seller refuses every payment itself until it is restarted.
func TestASellerStartedBeforeItsFacilitatorTakesPaymentsOnceItIsUp(t *testing.T) {
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := reserved.Addr().String()
	require.NoError(t, reserved.Close())

	sellerURL, _ := weatherSeller(t, "http://"+addr, func(cfg *sellerConfig) {
		cfg.ratePerSecond = 1000
		cfg.rateBurst = 1000
	})

	txB64, account := exacttest.SignedTx(t, nil)
	node := &exacttest.Node{Account: account, SimulateErr: std.ErrInsufficientFunds("insufficient funds")}
	gno := facilitator.New(node, exacttest.ChainID).Handler()
	var verifies atomic.Int32
	listener, err := net.Listen("tcp", addr)
	require.NoError(t, err)
	gnofacilitator := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/verify" {
				verifies.Add(1)
			}
			gno.ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: time.Second,
	}
	go func() { _ = gnofacilitator.Serve(listener) }()
	t.Cleanup(func() { _ = gnofacilitator.Close() })

	require.Eventually(t, func() bool {
		payWith(t, sellerURL, txB64)
		return verifies.Load() > 0
	}, 10*time.Second, 250*time.Millisecond, "the payment never reached the facilitator")
}
