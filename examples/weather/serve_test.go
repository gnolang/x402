package main

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureLogs sends what the default logger writes to a buffer for the rest of
// the test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

// TestStoppingDrainsAPaidRequestInFlight stops the seller while a request is
// running, as a paid one may be between its settlement and its response. It
// finishes and gets its answer before serve returns.
func TestStoppingDrainsAPaidRequestInFlight(t *testing.T) {
	logs := captureLogs(t)
	entered, release := make(chan struct{}), make(chan struct{})
	srv := newServer("", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusOK)
	}))
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
		_ = res.Body.Close()
		status <- res.StatusCode
	}()

	<-entered
	stop()
	// The listener closes once the stop has begun; the request is still running.
	require.Eventually(t, func() bool {
		conn, err := net.Dial("tcp", ln.Addr().String())
		if err == nil {
			_ = conn.Close()
		}
		return err != nil
	}, 5*time.Second, 10*time.Millisecond, "the seller kept accepting connections after the stop")
	require.Empty(t, served, "serve returned while a request was still in flight")

	close(release)
	assert.Equal(t, http.StatusOK, <-status)
	require.NoError(t, <-served)
	assert.Contains(t, logs.String(), "draining requests in flight", "an operator sees the stop begin")
}

// A request still running when the drain window closes makes the stop an
// error, so the exit code says the stop was not clean.
func TestStoppingReportsADrainThatRanOut(t *testing.T) {
	captureLogs(t)
	entered, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv := newServer("", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(entered)
		<-release
	}))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	served := make(chan error, 1)
	go func() { served <- serve(ctx, srv, ln, 100*time.Millisecond) }()

	go func() {
		if res, err := http.Get("http://" + ln.Addr().String()); err == nil {
			_ = res.Body.Close()
		}
	}()

	<-entered
	stop()
	require.ErrorIs(t, <-served, context.DeadlineExceeded)
}

// A connection accepted just before a stop has readHeaderTimeout to send its
// headers, then the middleware gives the request paymentTimeout. The drain
// covers both, or a stop could still cut a paid request short.
func TestTheDrainOutlastsAPaidRequest(t *testing.T) {
	assert.GreaterOrEqual(t, drainTimeout, readHeaderTimeout+paymentTimeout)
}
