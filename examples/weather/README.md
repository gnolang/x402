# weather — an HTTP resource that takes gno

The x402 seller example, with gno listed as a way to pay. The resource is weather data: it is not
a realm, it makes no contract call, and it does not know a chain exists. Everything gno-specific is
a network string, one registered scheme and the page a browser gets.

```go
routes := x402http.RoutesConfig{
    "GET /weather": {
        Accepts: x402http.PaymentOptions{{
            Scheme: "exact", PayTo: payTo,
            Price:   x402.AssetAmount{Asset: "ugnot", Amount: "250000"},
            Network: "gno:dev",
        }},
        Description: "Weather data",
    },
}

handler := nethttpmw.X402Payment(nethttpmw.Config{
    Routes:      routes,
    Facilitator: x402http.NewHTTPFacilitatorClient(&x402http.FacilitatorConfig{URL: facilitator}),
    Schemes:     []nethttpmw.SchemeConfig{{Network: "gno:dev", Server: gnoexact.NewExactGnoScheme()}},
})(mux)
```

The middleware, the route matching, the 402 and the header encoding are all the ecosystem's. Only
`gnoexact.NewExactGnoScheme()` and the page a browser gets are ours.

## Run it

Needs Go, Node 22.18+ or 24.11+, and `gnodev` (`make install.gnodev` in a
[gnolang/gno](https://github.com/gnolang/gno) checkout). From this directory, one command per
terminal:

```sh
gnodev              # a local node (chain id "dev", RPC :26657)
make facilitator    # the facilitator, on :8402
make run            # the seller, on :8401
make pay            # buy.mjs pays it, as gno's public test1 key
```

`make pay` prints the payment and the weather it bought:

```text
buying as g1jg8mtutu9khhfwc4nxmuhcpftf0pajdhfvsqf5, gno's public test1 key; set X402_MNEMONIC to pay from your own account
paid: tx a0a66524832960c26ad3110d998f8ad87c3ee1166ca1faac145e7e1ee9ab8b8f by g1jg8mtutu9khhfwc4nxmuhcpftf0pajdhfvsqf5
{"celsius":24,"forecast":"sunny","location":"Paris"}
```

### On the onyx testnet

The seller uses the public facilitator for `onyx-1`, so no local node or facilitator runs. The buyer
pays from your own key, funded at [faucet.gno.land](https://faucet.gno.land). Use a throwaway key:
the mnemonic stays in your shell history.

```sh
make run NETWORK=gno:onyx-1 FACILITATOR=https://facilitator.onyx.testnets.gno.land PAY_TO=g1...
X402_MNEMONIC="<funded mnemonic>" make pay RPC=https://rpc.onyx.testnets.gno.land:443
```

To settle through your own facilitator instead, start it with
`make facilitator NETWORK=gno:onyx-1 RPC=https://rpc.onyx.testnets.gno.land:443` and leave
`FACILITATOR` at its default.

## Run it yourself, piece by piece

Three processes, started from the repository root. The facilitator talks to the chain; the seller
never does.

**1. A gno node**, with its RPC reachable. Any node works — a local dev chain or a testnet.

**2. The facilitator.** `-chain-id` must name the chain `-rpc` actually serves; it checks at startup
and exits rather than blame payers for an operator's mismatch.

```sh
go run ./cmd/gnofacilitator -rpc <RPC URL> -chain-id <CHAIN ID> -listen :8402
```

**3. The seller.** `-network` is `gno:` plus that same chain id.

```sh
go run ./examples/weather \
  -pay-to g1u7y667z64x2h7vc6fmpcprgey4ck233jaww9zq \
  -network gno:<CHAIN ID> \
  -price 250000ugnot \
  -facilitator http://localhost:8402
```

`-price` is parsed by the chain's own coin parser, so it takes whatever `gnokey` would.

## What you get

```sh
curl -i http://localhost:8401/weather
```

`402`, with the terms in the `PAYMENT-REQUIRED` header as base64 JSON — that is where v2 puts them,
not in the body:

```json
{
  "x402Version": 2,
  "error": "Payment required",
  "resource": {
    "url": "http://localhost:8401/weather",
    "description": "Weather data",
    "mimeType": "application/json"
  },
  "accepts": [{
    "scheme": "exact",
    "network": "gno:dev",
    "asset": "ugnot",
    "amount": "250000",
    "payTo": "g1u7y667z64x2h7vc6fmpcprgey4ck233jaww9zq",
    "maxTimeoutSeconds": 300,
    "extra": { "areFeesSponsored": false }
  }]
}
```

`areFeesSponsored: false` says the payer pays the network fee inside the transaction they sign, and
the facilitator holds no key. The mechanism sets it, so no seller writes it. There is no
`paymentFlow` key because the flow is `authorization`, the specification's default, which may be
omitted.

A browser gets an HTML page naming the price, the network and the payee, which `main.go` builds.
The middleware's own paywall supports EVM and Solana chains only.

## Paying it

There is no Go client mechanism, so a Go buyer cannot pay this. `buy.mjs` can, from the repository
root:

```sh
make js               # build the client mechanism buy.mjs imports
X402_GNO_RPC=<RPC URL> node examples/weather/buy.mjs http://localhost:8401/weather
```

It registers `ExactGnoScheme` with a stock `@x402/fetch` client, admits ugnot to the client's spend
controls with a 1 GNOT cap per request, and signs with the mnemonic in `X402_MNEMONIC`, or as gno's
public test1 key when that is unset. The account must be **funded** on the chain the offer names
([faucet.gno.land](https://faucet.gno.land) funds the public testnets). Point `X402_GNO_RPC` at the
same node the facilitator uses: the buyer reads its account sequence from the chain, so this leg
needs a real node rather than the unreachable one a 402 tolerates.

The loop is covered: the e2e suite runs this seller configuration and this same `buy.mjs` against
an in-process node on every pull request, and asserts the seller's balance moved.

## What happens when you pay

`authorization` means the payment is checked, then the resource runs, then the payment is taken.
Two consequences worth knowing, both visible in `main.go`:

- **A resource that fails is never charged for.** The middleware buffers the response and skips
  settlement on any status ≥400.
- **A settlement that fails withholds the response.** The buffered body is discarded, so the cost
  of a failed settlement is the work already done rather than the data. The `ErrorHandler` is where
  a seller sees it; `SettlementHandler` is where they see the transaction that paid them.
