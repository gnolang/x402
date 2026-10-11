// Command weather is a seller: one ordinary HTTP resource, priced, with gno
// listed as a way to pay for it.
//
// It is the x402.org front-page example with a gno accepts[] entry. The resource
// is weather data and knows nothing about the chain — no realm, no contract call.
// Everything gno-specific is two strings, one registered scheme, and the page a
// browser gets in place of the resource.
//
//	weather -pay-to g1... -network gno:dev -price 250000ugnot
//
// Run a facilitator first; see the README next to this file.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gnolang/gno/tm2/pkg/std"
	x402 "github.com/x402-foundation/x402/go/v2"
	x402http "github.com/x402-foundation/x402/go/v2/http"
	nethttpmw "github.com/x402-foundation/x402/go/v2/http/nethttp"

	gnoexact "github.com/gnoverse/x402/server/exact"
)

// The facilitator is queried while the middleware is being built, so this bounds
// startup rather than a request.
const facilitatorSyncTimeout = 10 * time.Second

const readHeaderTimeout = 5 * time.Second

// paywall is what a browser gets in place of the resource. The middleware's own
// paywall supports EVM and Solana chains only.
var paywall = template.Must(template.New("paywall").Parse(`<!doctype html>
<html lang="en">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Payment required</title>
<h1>Payment required</h1>
<p>Weather data costs <strong>{{.Price}}</strong> on <code>{{.Network}}</code>, paid to <code>{{.PayTo}}</code>.</p>
<p>Pay it with an x402 client, such as
<a href="https://github.com/gnoverse/x402/tree/main/examples/weather">buy.mjs</a>:</p>
<pre>node buy.mjs &lt;this page's URL&gt;</pre>
</html>
`))

func main() {
	listen := flag.String("listen", ":8401", "HTTP listen address")
	facilitator := flag.String("facilitator", "http://localhost:8402", "gno facilitator base URL")
	network := flag.String("network", "gno:dev", "CAIP-2 network name, gno:<chain-id>")
	payTo := flag.String("pay-to", "", "gno address paid for each request (required)")
	price := flag.String("price", "250000ugnot", "price per request, as amount and denomination")
	flag.Parse()

	if *payTo == "" {
		fmt.Fprintln(os.Stderr, "weather: -pay-to is required; nobody is paid otherwise")
		os.Exit(2)
	}

	// gno prices are coins, so the chain's own parser decides what is payable and
	// this example never invents a second syntax for an amount.
	coin, err := std.ParseCoin(*price)
	if err != nil {
		fmt.Fprintf(os.Stderr, "weather: -price %q is not a gno coin: %v\n", *price, err)
		os.Exit(2)
	}

	seller, err := newSeller(sellerConfig{
		facilitatorURL: *facilitator,
		network:        *network,
		payTo:          *payTo,
		price:          coin,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "weather: %v\n", err)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:              *listen,
		Handler:           seller,
		ReadHeaderTimeout: readHeaderTimeout,
	}

	slog.Info("weather seller listening",
		"addr", *listen, "network", *network, "price", coin.String(), "payTo", *payTo)
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}

type sellerConfig struct {
	facilitatorURL string
	network        string // gno:<chain-id>
	payTo          string
	price          std.Coin
}

// newSeller returns the weather resource behind the x402 middleware. It asks the
// facilitator what it supports before returning: an unreachable facilitator is
// only warned about, and one without exact on cfg.network exits the process.
func newSeller(cfg sellerConfig) (http.Handler, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /weather", serveWeather)

	var page strings.Builder
	err := paywall.Execute(&page, struct{ Price, Network, PayTo string }{
		Price:   cfg.price.String(),
		Network: cfg.network,
		PayTo:   cfg.payTo,
	})
	if err != nil {
		return nil, fmt.Errorf("render the paywall: %w", err)
	}

	routes := x402http.RoutesConfig{
		"GET /weather": {
			Accepts: x402http.PaymentOptions{{
				Scheme:  "exact",
				PayTo:   cfg.payTo,
				Price:   x402.AssetAmount{Asset: cfg.price.Denom, Amount: fmt.Sprint(cfg.price.Amount)},
				Network: x402.Network(cfg.network),
			}},
			Description:       "Weather data",
			MimeType:          "application/json",
			CustomPaywallHTML: page.String(),
		},
	}

	return nethttpmw.X402Payment(nethttpmw.Config{
		Routes:      routes,
		Facilitator: x402http.NewHTTPFacilitatorClient(&x402http.FacilitatorConfig{URL: cfg.facilitatorURL}),
		Schemes: []nethttpmw.SchemeConfig{
			{Network: x402.Network(cfg.network), Server: gnoexact.NewExactGnoScheme()},
		},
		SyncFacilitatorOnStart: true,
		Timeout:                facilitatorSyncTimeout,

		// Settlement runs after the resource has produced its response, so this is
		// where a seller learns they were actually paid.
		SettlementHandler: func(_ http.ResponseWriter, _ *http.Request, resp *x402.SettleResponse) {
			slog.Info("paid", "tx", resp.Transaction, "payer", resp.Payer, "network", resp.Network)
		},

		// And this is where they learn they were not. The buyer is not served: the
		// middleware buffers the resource and discards it when settlement fails, so
		// the cost of a failure here is the work already done, not the data.
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			slog.Error("not paid; the response is withheld", "err", err)
			w.WriteHeader(http.StatusPaymentRequired)
		},
	})(mux), nil
}

func serveWeather(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"location": "Paris",
		"forecast": "sunny",
		"celsius":  24,
	})
}
