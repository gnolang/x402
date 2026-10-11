// Command gnofacilitator serves the x402 facilitator API (verify/settle/
// supported) for a single gno chain. It holds no keys: it only relays
// client-signed transactions. EXPERIMENTAL.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	gnoclient "github.com/gnolang/gno/gno.land/pkg/gnoclient"
	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"

	"github.com/gnoverse/x402/facilitator"
	"github.com/gnoverse/x402/facilitator/exact"
)

// version is stamped at build time with -ldflags "-X main.version=…". A binary
// that reports "dev" was built without one, which is the honest answer: an
// operator debugging a settlement needs to know which build refused it, and
// guessing from a file date is not knowing.
var version = "dev"

// Server timeouts, chosen to bound a slow or hostile client without
// interrupting a legitimate verify/settle round trip against the chain.
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 60 * time.Second

	// answerTimeout bounds a request's wait on the node. It stays below
	// writeTimeout so the 503 it produces can still be written.
	answerTimeout = 25 * time.Second
)

// How long the startup chain-id check waits for a node to answer, in total.
//
// A facilitator is normally started alongside its node — the same compose file,
// the same unit, the same container — so the first query usually lands before the
// node is listening. Asking once would make the check permanently useless in
// exactly the deployment it ships in: it would warn every time and never compare
// anything. Retrying briefly makes it real without holding startup hostage to a
// node that is genuinely down, which is reported and served through (every request
// then answers 503, which is the honest answer anyway).
//
// The window bounds the whole check, including a single query that never returns.
const (
	chainIDCheckWindow   = 10 * time.Second
	chainIDCheckInterval = 500 * time.Millisecond
)

// nodeChainID asks the node which chain it serves, within chainIDCheckWindow
// whatever the node does.
//
// The bound is a watchdog because a single query waits up to tm2's own 60 s
// request timeout. Startup would wait that long on a node that accepts the
// connection and never answers, with nothing listening.
//
// The query is left running if the watchdog fires. It answers into a buffered
// channel nobody reads, so it cannot block on the send, and the process is either
// serving by then or on its way out.
func nodeChainID(node *exact.GnoclientNode) (string, error) {
	type answer struct {
		chainID string
		err     error
	}
	answered := make(chan answer, 1)
	go func() {
		chainID, err := retryChainID(node)
		answered <- answer{chainID: chainID, err: err}
	}()

	select {
	case got := <-answered:
		return got.chainID, got.err
	case <-time.After(chainIDCheckWindow):
		return "", fmt.Errorf("the node did not answer the chain-id query within %s", chainIDCheckWindow)
	}
}

// retryChainID asks until the node answers or the window is spent, so a
// facilitator started beside its own node does not warn on the first query and
// then never compare anything. The last error is returned once the window is spent.
func retryChainID(node *exact.GnoclientNode) (string, error) {
	deadline := time.Now().UTC().Add(chainIDCheckWindow)
	for attempt := 0; ; attempt++ {
		reported, err := node.NodeChainID(context.Background())
		if err == nil {
			if attempt > 0 {
				slog.Info("node answered the chain-id check", "attempts", attempt+1)
			}
			return reported, nil
		}
		if time.Now().UTC().After(deadline) {
			return "", err
		}
		time.Sleep(chainIDCheckInterval)
	}
}

func main() {
	listen := flag.String("listen", ":8402", "HTTP listen address")
	rpcURL := flag.String("rpc", "", "gno chain RPC URL (required)")
	chainID := flag.String("chain-id", "", "chain id served as network gno:<chain-id> (required)")
	ratePerSecond := flag.Float64("rate-per-second", facilitator.DefaultRatePerSecond, "sustained per-peer requests/second on /verify and /settle (0 = default)")
	rateBurst := flag.Int("rate-burst", facilitator.DefaultRateBurst, "largest per-peer request spike tolerated on /verify and /settle (0 = default)")
	showVersion := flag.Bool("version", false, "print the build version and exit")
	flag.Parse()

	// Answered before the required flags are enforced: asking a binary what it is
	// must not require knowing how to run it.
	if *showVersion {
		fmt.Println(version)
		return
	}

	if *rpcURL == "" || *chainID == "" {
		slog.Error("-rpc and -chain-id are required")
		os.Exit(2)
	}
	// The network this serves is "gno:" + the flag, so a chain id the CAIP-2 form
	// cannot carry has to be refused here: every payment would be refused instead,
	// and none of those refusals would name the flag.
	if err := exact.ValidChainID(*chainID); err != nil {
		slog.Error("-chain-id cannot be published as a network name", "err", err)
		os.Exit(2)
	}
	if *ratePerSecond < 0 || *rateBurst < 0 {
		slog.Error("-rate-per-second and -rate-burst must not be negative")
		os.Exit(2)
	}

	rpc, err := rpcclient.NewHTTPClient(*rpcURL)
	if err != nil {
		slog.Error("rpc client", "err", err)
		os.Exit(1)
	}
	node := exact.NewGnoclientNode(&gnoclient.Client{RPCClient: rpc})

	// -chain-id and -rpc are configured separately, and a disagreement is
	// undetectable from a payment: every signature would be verified against the
	// wrong sign doc and refused as invalid, blaming payers for an operator error.
	// An answered mismatch is fatal; a query that never answered is not evidence of
	// one, so it warns rather than keeping the service from starting during a node
	// outage.
	switch reported, err := nodeChainID(node); {
	case err != nil:
		slog.Warn("cannot confirm the node's chain id; a mismatch would refuse every payment",
			"configured", *chainID, "err", err)
	case reported != *chainID:
		slog.Error("the node serves a different chain than -chain-id names",
			"configured", *chainID, "node", reported)
		os.Exit(1)
	}

	f := facilitator.New(node, *chainID,
		facilitator.WithRateLimit(facilitator.RateLimit{PerSecond: *ratePerSecond, Burst: *rateBurst}))

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		slog.Error("listen", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	slog.Info("gnofacilitator listening",
		"version", version, "addr", ln.Addr().String(), "network", "gno:"+*chainID)
	if err := serve(ctx, newServer(f.Handler(), answerTimeout), ln, writeTimeout); err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
	slog.Info("gnofacilitator stopped")
}

// newServer returns the HTTP server for h. A request still unanswered after
// answerWithin gets a 503, never a payment verdict. The handler keeps running
// behind that answer, so a /settle broadcast may still commit.
func newServer(h http.Handler, answerWithin time.Duration) *http.Server {
	return &http.Server{
		Handler:           http.TimeoutHandler(h, answerWithin, "the chain did not answer in time"),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
}

// serve runs srv on ln until ctx is done, then stops accepting connections and
// waits up to drain for the requests in flight. A drain that runs out is an
// error.
func serve(ctx context.Context, srv *http.Server, ln net.Listener, drain time.Duration) error {
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	select {
	case err := <-served:
		return err
	case <-ctx.Done():
	}
	slog.Info("gnofacilitator stopping: draining requests in flight", "within", drain)

	drainCtx, cancel := context.WithTimeout(context.Background(), drain)
	defer cancel()
	if err := srv.Shutdown(drainCtx); err != nil {
		return fmt.Errorf("drain: %w", err)
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
