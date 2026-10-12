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
	"errors"
	"flag"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gnolang/gno/tm2/pkg/std"
	x402 "github.com/x402-foundation/x402/go/v2"
	x402http "github.com/x402-foundation/x402/go/v2/http"
	nethttpmw "github.com/x402-foundation/x402/go/v2/http/nethttp"

	gnoexact "github.com/gnoverse/x402/server/exact"
)

// paymentTimeout bounds a paid request as a whole: verify, the resource, and a
// settle that can wait up to 10 s for a block, then run once more when the
// facilitator answers settlement_pending. It also bounds the facilitator query
// made while the middleware is built.
const paymentTimeout = 60 * time.Second

// Server timeouts. The write timeout runs from the end of the request's headers
// to the end of the response, so it leaves a paid request its paymentTimeout
// and room to write the answer.
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	writeTimeout      = paymentTimeout + 10*time.Second
	idleTimeout       = 60 * time.Second
)

// weatherPath is where the resource is served, and the path a public URL is
// extended with.
const weatherPath = "/weather"

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
	publicURL := flag.String("public-url", "", "base URL buyers reach this seller at, such as https://weather.example behind a TLS proxy (default: the address each request arrived on)")
	ratePerSecond := flag.Float64("rate-per-second", defaultRatePerSecond, "sustained requests/second on the priced route, all callers together (0 = default)")
	rateBurst := flag.Int("rate-burst", defaultRateBurst, "largest request spike tolerated on the priced route, all callers together (0 = default)")
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
		log:            slog.Default(),
		publicURL:      *publicURL,
		ratePerSecond:  *ratePerSecond,
		rateBurst:      *rateBurst,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "weather: %v\n", err)
		os.Exit(1)
	}

	srv := newServer(*listen, seller)

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
	log            *slog.Logger
	publicURL      string  // base URL buyers reach the seller at; "" names each request's own address
	ratePerSecond  float64 // backstop on the priced route; <=0 takes defaultRatePerSecond
	rateBurst      int     // <=0 takes defaultRateBurst
}

func newServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
}

// newSeller returns the weather resource behind the x402 middleware, behind a
// rate limit. It asks the facilitator what it supports before returning: an
// unreachable facilitator is only warned about, and one without exact on
// cfg.network exits the process.
func newSeller(cfg sellerConfig) (http.Handler, error) {
	route := "GET " + weatherPath
	mux := http.NewServeMux()
	mux.HandleFunc(route, serveWeather)

	// Behind a TLS proxy the middleware would name an http:// URL on the
	// seller's own address, so the 402 names the public one instead.
	var resource string
	if cfg.publicURL != "" {
		base, err := url.Parse(cfg.publicURL)
		if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" ||
			base.User != nil || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" {
			return nil, fmt.Errorf("-public-url %q is not a plain absolute http or https URL", cfg.publicURL)
		}
		resource = base.JoinPath(weatherPath).String()
	}

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
		route: {
			Resource: resource,
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

	server := x402.Newx402ResourceServer(
		x402.WithFacilitatorClient(x402http.NewHTTPFacilitatorClient(&x402http.FacilitatorConfig{URL: cfg.facilitatorURL})),

		// The middleware answers a payment the facilitator refuses with a 402 and
		// calls no handler for it, so this hook is the only place a seller learns of
		// it. A payment that never reached the facilitator does not get here.
		x402.WithOnVerifyFailureHook(func(failure x402.VerifyFailureContext) (*x402.VerifyFailureHookResult, error) {
			if refusal, ok := errors.AsType[*x402.VerifyError](failure.Error); ok {
				cfg.log.Warn("payment refused",
					"reason", refusal.InvalidReason, "payer", refusal.Payer, "detail", refusal.InvalidMessage)
			} else {
				cfg.log.Error("payment not checked", "err", failure.Error)
			}
			return nil, nil
		}),
	).Register(x402.Network(cfg.network), gnoexact.NewExactGnoScheme())

	paid := nethttpmw.PaymentMiddleware(routes, server,
		nethttpmw.WithSyncFacilitatorOnStart(true),
		nethttpmw.WithTimeout(paymentTimeout),

		// Settlement runs after the resource has produced its response, so this is
		// where a seller learns they were actually paid.
		nethttpmw.WithSettlementHandler(func(_ http.ResponseWriter, _ *http.Request, resp *x402.SettleResponse) {
			cfg.log.Info("paid", "tx", resp.Transaction, "payer", resp.Payer, "network", resp.Network)
		}),

		// And this is where they learn a settlement failed. The buyer is not served:
		// the middleware buffers the resource and discards it when settlement fails,
		// so the cost of a failure here is the work already done, not the data.
		nethttpmw.WithErrorHandler(func(w http.ResponseWriter, _ *http.Request, err error) {
			cfg.log.Error("not paid; the response is withheld", "err", err)
			w.WriteHeader(http.StatusPaymentRequired)
		}),
	)(mux)

	// The limit runs before the middleware, so a refused request costs the
	// facilitator nothing.
	limited := http.NewServeMux()
	limited.Handle(route, newRateLimit(cfg.ratePerSecond, cfg.rateBurst).guard(paid))
	return limited, nil
}

func serveWeather(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"location": "Paris",
		"forecast": "sunny",
		"celsius":  24,
	})
}
