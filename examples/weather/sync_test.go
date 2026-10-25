package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gnolang/gno/tm2/pkg/std"
	"github.com/stretchr/testify/require"

	"github.com/gnolang/x402/facilitator"
	"github.com/gnolang/x402/facilitator/exact/exacttest"
)

const (
	syncedMsg    = "facilitator synced; payments are accepted"
	notSyncedMsg = "facilitator not synced; payments are refused until it is"
)

// unusedAddr returns a local address nothing listens at.
func unusedAddr(t *testing.T) string {
	t.Helper()
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := reserved.Addr().String()
	require.NoError(t, reserved.Close())
	return addr
}

// TestASellerStartedBeforeItsFacilitatorTakesPaymentsOnceItIsUp starts the
// seller while nothing listens at the facilitator's address, then starts the
// facilitator there. The startup sync is what tells the middleware which
// facilitator serves the network, and upstream makes it once: without a retry,
// the seller refuses every payment itself until it is restarted.
func TestASellerStartedBeforeItsFacilitatorTakesPaymentsOnceItIsUp(t *testing.T) {
	addr := unusedAddr(t)
	sellerURL, log := weatherSeller(t, "http://"+addr, func(cfg *sellerConfig) {
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

	require.Eventually(t, func() bool { return log.logged(syncedMsg) },
		10*time.Second, 50*time.Millisecond, "the seller never synced with its facilitator")
	payWith(t, sellerURL, txB64)
	require.Equal(t, int32(1), verifies.Load(), "the payment never reached the facilitator")
}

// TestASellerAnswersWhileItAsksAHungFacilitatorAgain starts the seller while
// nothing listens at the facilitator's address, then listens there without ever
// answering. The server holds its lock while it asks the facilitator, and every
// 402 is built under that lock, so the question that may hang must be asked
// outside it.
func TestASellerAnswersWhileItAsksAHungFacilitatorAgain(t *testing.T) {
	addr := unusedAddr(t)
	sellerURL, _ := weatherSeller(t, "http://"+addr)

	hung, err := net.Listen("tcp", addr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = hung.Close() })
	asked := make(chan net.Conn, 1)
	go func() {
		if conn, err := hung.Accept(); err == nil {
			asked <- conn
		}
	}()
	select {
	case conn := <-asked:
		t.Cleanup(func() { _ = conn.Close() })
	case <-time.After(10 * time.Second):
		t.Fatal("the seller never asked the facilitator again")
	}

	client := &http.Client{Timeout: 2 * time.Second}
	unpaid, err := client.Get(sellerURL + "/weather")
	require.NoError(t, err, "the 402 waited on the question to the facilitator")
	require.NoError(t, unpaid.Body.Close())
	require.Equal(t, http.StatusPaymentRequired, unpaid.StatusCode)
}

// TestASellerSaysWhenItAsksItsFacilitatorNext reads the first warning of a
// seller whose facilitator is down: it names the wait before the next attempt,
// which is twice the one before it.
func TestASellerSaysWhenItAsksItsFacilitatorNext(t *testing.T) {
	_, log := weatherSeller(t, "http://"+unusedAddr(t))

	require.Eventually(t, func() bool { return log.logged(notSyncedMsg) },
		10*time.Second, 50*time.Millisecond, "the seller never retried")
	warnings := log.recordsWith(t, "retry_in")
	require.NotEmpty(t, warnings)
	require.InDelta(t, float64(2*syncRetryMin), warnings[0]["retry_in"], 0)
}

// TestASellerStopsAskingItsFacilitatorWhenItsContextEnds keeps the facilitator
// failing, so the seller keeps asking, until the context newSeller was given
// ends.
func TestASellerStopsAskingItsFacilitatorWhenItsContextEnds(t *testing.T) {
	var asked atomic.Int32
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked.Add(1)
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(down.Close)

	ctx, stop := context.WithCancel(t.Context())
	_, err := newSeller(ctx, sellerConfig{
		facilitatorURL: down.URL,
		network:        "gno:" + exacttest.ChainID,
		payTo:          exacttest.PayTo,
		price:          std.MustParseCoin("250000ugnot"),
		log:            slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)

	// Once at startup, once more after syncRetryMin.
	require.Eventually(t, func() bool { return asked.Load() >= 2 },
		10*time.Second, 50*time.Millisecond, "the seller never asked again")
	stop()
	seen := asked.Load()
	require.Never(t, func() bool { return asked.Load() > seen },
		3*syncRetryMin, 100*time.Millisecond, "the seller kept asking after its context ended")
}
