# x402 on gno.land

[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)
[![Container](https://img.shields.io/badge/ghcr.io-gnolang%2Fgnofacilitator-2496ED?logo=docker&logoColor=white)](https://github.com/gnolang/x402/pkgs/container/gnofacilitator)

Charge for an HTTP endpoint in ugnot (a millionth of a GNOT), and let any x402 client pay it.
[x402](https://x402.org) is the payment protocol behind HTTP 402: a server answers `402` with the
terms it accepts, a client pays, and the request succeeds. This repository adds gno.land as one of
those terms. The resource is any endpoint: no realm and no contract call are involved.

> [!WARNING]
> Nothing here is audited, and releases may break each other while the interfaces, the reason
> codes and the payload shape settle. Point it at testnets only, never at real funds.

The ecosystem's own middleware and clients handle the protocol, route matching, header encoding
and the retry. This repository supplies the parts that know about gno:

| Part | Path | What it does |
|---|---|---|
| Seller mechanism | `server/exact/` | Lets a route list gno in `accepts[]`, so the middleware prices it, offers it and reads the payment |
| Facilitator | `cmd/gnofacilitator/`, `facilitator/` | Verifies payments and broadcasts them. It holds no keys |
| Client mechanism | `client/` (TypeScript), `client/exact/` (Go) | Teaches a stock x402 client to pay a gno chain |

[`examples/weather/`](examples/weather) is a seller and a buyer that make one payment end to end.

## Try it

<https://x402-demo.onyx.testnets.gno.land/weather> sells sample weather for 0.25 GNOT (250000ugnot)
on the onyx testnet (`gno:onyx-1`). A browser with the [Adena](https://adena.app) wallet pays from
the page, and a terminal pays with the two commands in
[examples/weather](examples/weather#paying-it).

## Sell something

A seller lists gno in a route's `accepts[]` and registers the gno scheme on the stock middleware:

```go
routes := x402http.RoutesConfig{
    "GET /weather": {
        Accepts: x402http.PaymentOptions{{
            Scheme:  "exact",
            PayTo:   "g1u7y667z64x2h7vc6fmpcprgey4ck233jaww9zq",
            Price:   x402.AssetAmount{Asset: "ugnot", Amount: "250000"},
            Network: "gno:dev",
        }},
        Description: "Weather data",
    },
}

handler := nethttpmw.X402Payment(nethttpmw.Config{
    Routes:      routes,
    Facilitator: x402http.NewHTTPFacilitatorClient(&x402http.FacilitatorConfig{URL: facilitatorURL}),
    Schemes:     []nethttpmw.SchemeConfig{{Network: "gno:dev", Server: gnoexact.NewExactGnoScheme()}},
})(mux)
```

An unpaid request gets `402`. v2 carries the terms in the `PAYMENT-REQUIRED` header as base64
JSON, not in the body:

```json
{ "x402Version": 2,
  "error": "Payment required",
  "resource": { "url": "http://localhost:8401/weather", "description": "Weather data",
                "mimeType": "application/json" },
  "accepts": [{
    "scheme": "exact", "network": "gno:dev", "asset": "ugnot", "amount": "250000",
    "payTo": "g1u7y667z64x2h7vc6fmpcprgey4ck233jaww9zq",
    "maxTimeoutSeconds": 300,
    "extra": { "areFeesSponsored": false }
  }] }
```

[examples/weather](examples/weather) runs this seller against a facilitator and a real chain.

A public facilitator serves gno.land's testnet at `https://facilitator.onyx.testnets.gno.land`.
Its network is `gno:onyx-1`, so a seller using it sets `Network` to that. You can also
[run your own](docs/deploying.md).

## Pay for it

Install the [client mechanism](client/README.md) from npm:

```sh
npm i @gnoverse/x402-gno
```

Register it on a stock x402 client and admit ugnot to the client's spend controls:

```js
import { ExactGnoScheme } from "@gnoverse/x402-gno/exact/client";

const client = new x402Client()
  .register("gno:*", new ExactGnoScheme(wallet))
  .setSpendControls({
    allowedAssets: [{ network: "gno:*", asset: "ugnot", maxAmountPerPayment: "1000000" }],
  });
const paid = wrapFetchWithPayment(fetch, client);
const res = await paid("https://api.example.com/weather");
```

From Go, register [`client/exact`](client/exact) on upstream's Go client:

```go
import clientexact "github.com/gnolang/x402/client/exact"

signer, _ := clientexact.KeybaseSigner(kb, "buyer", password)                // a key gnokey keeps
node, _ := clientexact.NewRPCNode("https://rpc.onyx.testnets.gno.land:443") // a node on onyx-1

client := x402.Newx402Client().
	Register("gno:onyx-1", clientexact.NewExactGnoScheme(signer, node)).
	SetSpendControls(x402.SpendControls{AllowedAssets: []x402.SpendControlAsset{
		{Network: "gno:onyx-1", Asset: "ugnot", MaxAmountPerPayment: "1000000"},
	}})
paid := x402http.WrapHTTPClientWithPayment(&http.Client{Timeout: time.Minute}, x402http.Newx402HTTPClient(client))
res, err := paid.Get("https://api.example.com/weather")
```

`@x402/core` from 2.23 and upstream's Go client pay only in USD-pegged assets unless their spend
controls list the asset, so the buyer lists ugnot. `maxAmountPerPayment` is the most one request
may cost, in ugnot; the network fee is paid on top. The stock client's own code, unmodified, does
the rest, from reading the 402 to the retry. `wallet` must be connected to a node on the chain the
offer names, since the account state the payment signs over comes from that node
([details](client/README.md#the-chain-id-comes-from-the-offer)).

An agent can pay with a [session key](client/README.md#paying-with-a-session-key) instead of its
owner's key, and the chain holds it to the spend limit and expiry the owner registered.

## How a payment works

The flow is x402's `authorization`: the facilitator verifies the payment, the seller's handler
runs, the facilitator settles the payment, and only then does the buyer get the response.

- **The payer pays the network fee.** `std.Fee` names no payer and the chain charges the first signer, so the payer of the resource also pays the gas. Offers state this as `extra.areFeesSponsored: false`.
- **The facilitator holds no key.** It verifies offline and broadcasts a transaction the payer already signed, so it cannot move funds anywhere the payer did not sign for.
- **A failed resource is not charged for.** The middleware buffers the response and skips settlement on any status ≥400. Skipping it does not void the payment, which stays signed and valid.
- **A failed settlement withholds the response.** The buffered body is discarded.
- **A payment no block executed in time is pending, not failed.** The facilitator answers
  `settlement_pending` with the transaction hash. The seller's SDK settles once more, which asks
  the chain about that hash rather than broadcasting again, and a buyer still not served gets the
  hash to look up.
- **A payment settles once per facilitator.** A copy presented while it settles, or within five
  minutes after, is refused as `duplicate_settlement`. The record is in memory and per process,
  so a facilitator runs as one process per chain, never as replicas behind a balancer.
- **Prices name their denomination.** gno has no default asset, so `"$0.001"` errors rather than resolving to a token.

Two exposures stay open:

- **A signed payment does not expire.** A gno transaction carries no expiry, and
  `maxTimeoutSeconds` binds nothing in it. Until the payer spends its sequence on another
  transaction, whoever holds the `PAYMENT-SIGNATURE` header, the seller included, can broadcast
  it. A buyer refused service voids an unused payment by sending any other transaction from the
  paying account.
- **A payer can consume the signed-over sequence between `/verify` and `/settle`.** Settlement
  then fails after the seller's handler has run, so the seller does the work unpaid. gno has no
  pull-settlement primitive, so settlement is cooperative, the same exposure XRPL documents for
  its own `exact` mechanism.

## Development

The main Go module and `package.json` both live at the repository root (`e2e/` is a second Go
module), so these run from here:

```bash
make test                # Library tests (no chain, no npm)
make js-test             # Client mechanism, including the sign-doc byte-equality check
make test-e2e            # Real payments against in-memory nodes
make lint                # go vet (both modules) + gofmt -l
make build               # bin/gnofacilitator
make help                # Every target
```

`make test-e2e` pays a priced endpoint through gno.land's txtar harness with the JS client and the
Go client, and asserts the balances changed. It also settles one payment twice at once in three
arrangements of facilitators and nodes, and replays a settled payment to assert it is refused.

Built against [`github.com/x402-foundation/x402/go/v2`](https://github.com/x402-foundation/x402) for
the protocol and [`gnolang/gno`](https://github.com/gnolang/gno) for the chain.

## Conformance

Implemented: the `exact` scheme, the `authorization` flow, v2 headers, `gno:<chain-id>` as a
CAIP-2 network, and prices in native units.

Not implemented: dollar-string pricing, gno in the middleware's browser paywall (the weather
example serves its own page), payload `extensions`, and a CAIP-2 namespace registration. The
schema does not require a registration: `near:testnet` ships without one.

## License

Apache-2.0. See [LICENSE](LICENSE).
