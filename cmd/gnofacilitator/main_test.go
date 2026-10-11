package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	gnoclient "github.com/gnolang/gno/gno.land/pkg/gnoclient"
	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gnoverse/x402/facilitator"
)

// silentNode accepts connections and never answers them, the way a node that
// is wedged rather than down behaves.
func silentNode(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	var (
		mu    sync.Mutex
		conns []net.Conn
	)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range conns {
			conn.Close()
		}
	})
	return "http://" + ln.Addr().String()
}

// jsSignedPayment returns a request body carrying the facilitator package's
// JS-signed fixture, a payment that passes every offline check and so reaches the
// node.
func jsSignedPayment(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "facilitator", "testdata", "gnojs_signed_send.b64"))
	require.NoError(t, err)

	req := facilitator.PaymentRequirements{
		Scheme:            "exact",
		Network:           "gno:dev",
		Amount:            "250000",
		Asset:             "ugnot",
		PayTo:             "g1u7y667z64x2h7vc6fmpcprgey4ck233jaww9zq",
		MaxTimeoutSeconds: 300,
		Extra:             map[string]any{"memo": "x402-interop"},
	}
	body, err := json.Marshal(facilitator.Request{
		X402Version: 2,
		PaymentPayload: facilitator.PaymentPayload{
			X402Version: 2,
			Accepted:    req,
			Payload:     facilitator.SchemePayload{Transaction: strings.TrimSpace(string(raw))},
		},
		PaymentRequirements: req,
	})
	require.NoError(t, err)
	return string(body)
}

// TestSilentNodeIsAnswered503 pins the promise a seller relies on: a payment
// the chain never answered for gets a 503, not a dropped connection. The answer
// deadline is what delivers it, since a node call can outlast the write timeout.
func TestSilentNodeIsAnswered503(t *testing.T) {
	rpc, err := rpcclient.NewHTTPClient(silentNode(t), rpcclient.WithRequestTimeout(5*time.Second))
	require.NoError(t, err)
	node := facilitator.NewGnoclientNode(&gnoclient.Client{RPCClient: rpc})

	const answerWithin = 100 * time.Millisecond
	srv := newServer("", facilitator.New(node, "dev").Handler(), answerWithin)
	ts := httptest.NewUnstartedServer(srv.Handler)
	ts.Config = srv
	ts.Start()
	t.Cleanup(ts.Close)

	for _, path := range []string{"/verify", "/settle"} {
		t.Run(path, func(t *testing.T) {
			start := time.Now().UTC()
			res, err := http.Post(ts.URL+path, "application/json", strings.NewReader(jsSignedPayment(t)))
			require.NoError(t, err)
			defer res.Body.Close()
			body, err := io.ReadAll(res.Body)
			require.NoError(t, err)

			assert.Equal(t, http.StatusServiceUnavailable, res.StatusCode)
			assert.NotContains(t, string(body), "isValid", "a deadline is not a verdict")
			assert.Less(t, time.Since(start), 2*time.Second, "answered by the deadline, not the node call's own timeout")
		})
	}
}

// TestServeDrainsAnInFlightRequest pins that a request in flight when the stop
// arrives, such as a settle the chain may be committing, finishes and gets its
// answer before serve returns.
func TestServeDrainsAnInFlightRequest(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusOK)
	})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	served := make(chan error, 1)
	go func() { served <- serve(ctx, srv, ln, 5*time.Second) }()

	status := make(chan int, 1)
	go func() {
		res, err := http.Get("http://" + ln.Addr().String())
		if err != nil {
			status <- 0
			return
		}
		res.Body.Close()
		status <- res.StatusCode
	}()

	<-entered
	stop()
	require.Never(t, func() bool { return len(served) > 0 }, 200*time.Millisecond, 20*time.Millisecond,
		"serve returned while a request was still in flight")

	close(release)
	assert.Equal(t, http.StatusOK, <-status)
	require.NoError(t, <-served)
}

// TestServeReportsADrainThatRanOut pins that a request still running when the
// drain window closes makes the stop an error, so the exit code says the stop was
// not clean.
func TestServeReportsADrainThatRanOut(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
	})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	served := make(chan error, 1)
	go func() { served <- serve(ctx, srv, ln, 100*time.Millisecond) }()

	go func() {
		if res, err := http.Get("http://" + ln.Addr().String()); err == nil {
			res.Body.Close()
		}
	}()

	<-entered
	stop()
	require.ErrorIs(t, <-served, context.DeadlineExceeded)
}
