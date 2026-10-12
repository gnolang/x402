package main

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStoppingDrainsAPaidRequestInFlight stops the seller while a request is
// running, as a paid one may be between its settlement and its response. It
// finishes and gets its answer before serve returns.
func TestStoppingDrainsAPaidRequestInFlight(t *testing.T) {
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
	require.Never(t, func() bool { return len(served) > 0 }, 200*time.Millisecond, 20*time.Millisecond,
		"serve returned while a request was still in flight")

	close(release)
	assert.Equal(t, http.StatusOK, <-status)
	require.NoError(t, <-served)
}

// A request still running when the drain window closes makes the stop an
// error, so the exit code says the stop was not clean.
func TestStoppingReportsADrainThatRanOut(t *testing.T) {
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

// The drain covers the longest a request may run, or a stop could still cut a
// paid request short.
func TestTheDrainOutlastsAPaidRequest(t *testing.T) {
	assert.GreaterOrEqual(t, drainTimeout, writeTimeout)
}
