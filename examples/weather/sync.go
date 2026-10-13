package main

import (
	"context"
	"fmt"
	"time"

	x402 "github.com/x402-foundation/x402/go/v2"
)

// How often a seller whose startup sync failed asks the facilitator again: at
// first after syncRetryMin, then twice as long each time up to syncRetryMax.
// syncTimeout bounds each question.
const (
	syncRetryMin = time.Second
	syncRetryMax = 30 * time.Second
	syncTimeout  = 5 * time.Second
)

// keepSyncing asks facilitator what it serves, in the background, until it
// reports exact on cfg.network or ctx ends, and returns at once when the
// startup sync already succeeded.
//
// Upstream syncs once, while the middleware is built, and only warns when that
// fails: the middleware then knows no facilitator for the network and refuses
// every payment itself, while unpaid requests still get their 402, until the
// process restarts.
func keepSyncing(ctx context.Context, server *x402.X402ResourceServer, facilitator x402.FacilitatorClient, cfg sellerConfig) {
	network := x402.Network(cfg.network)
	if server.HasFacilitatorSupport(network, paymentScheme) {
		return
	}
	go func() {
		delay := syncRetryMin
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			err := syncOnce(ctx, server, facilitator)
			if server.HasFacilitatorSupport(network, paymentScheme) {
				cfg.log.Info("facilitator synced; payments are accepted", "network", network)
				return
			}
			if err == nil {
				err = fmt.Errorf("the facilitator serves no %s payments on %s", paymentScheme, network)
			}
			delay = min(2*delay, syncRetryMax)
			cfg.log.Warn("facilitator not synced; payments are refused until it is", "retry_in", delay, "err", err)
		}
	}()
}

// syncOnce has server ask facilitator what it serves, once facilitator has
// answered the same question. The server holds its lock while it asks, and every
// request on the priced route waits on that lock, unpaid ones included, so the
// question that may hang is asked first, outside it.
//
// ceiling: a facilitator that answers the first question and hangs on the second
// still holds every request for up to syncTimeout.
func syncOnce(ctx context.Context, server *x402.X402ResourceServer, facilitator x402.FacilitatorClient) error {
	probe, cancelProbe := context.WithTimeout(ctx, syncTimeout)
	defer cancelProbe()
	if _, err := facilitator.GetSupported(probe); err != nil {
		return err
	}
	locked, cancelLocked := context.WithTimeout(ctx, syncTimeout)
	defer cancelLocked()
	return server.Initialize(locked)
}
