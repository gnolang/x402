package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// drainTimeout is how long a stop waits for the requests in flight: the longest
// one may run. A paid request cut short after its settlement charges the buyer
// for a response they never get.
const drainTimeout = writeTimeout

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
	slog.Info("weather seller stopping: draining requests in flight", "within", drain)

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
