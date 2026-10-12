package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	x402 "github.com/x402-foundation/x402/go/v2"
)

// How often a seller whose startup sync failed asks the facilitator again: at
// first after syncRetryMin, then twice as long each time up to syncRetryMax.
const (
	syncRetryMin = time.Second
	syncRetryMax = 30 * time.Second
)

// keepSyncing asks the facilitator what it serves until it reports scheme on
// network, in the background, and returns at once when the startup sync
// already succeeded.
//
// Upstream syncs once, while the middleware is built, and only warns when that
// fails: the middleware then knows no facilitator for the network and refuses
// every payment itself, while unpaid requests still get their 402, until the
// process restarts.
func keepSyncing(server *x402.X402ResourceServer, network x402.Network, scheme string, log *slog.Logger) {
	if server.HasFacilitatorSupport(network, scheme) {
		return
	}
	go func() {
		delay := syncRetryMin
		for {
			time.Sleep(delay)
			ctx, cancel := context.WithTimeout(context.Background(), paymentTimeout)
			err := server.Initialize(ctx)
			cancel()
			if server.HasFacilitatorSupport(network, scheme) {
				log.Info("facilitator synced; payments are accepted", "network", network)
				return
			}
			if err == nil {
				err = fmt.Errorf("the facilitator serves no %s payments on %s", scheme, network)
			}
			log.Warn("facilitator not synced; payments are refused until it is", "retry_in", delay, "err", err)
			delay = min(2*delay, syncRetryMax)
		}
	}()
}
