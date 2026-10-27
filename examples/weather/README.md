# weather: an HTTP resource that takes gno

The x402 seller example, with gno listed as a way to pay. Each paid request gets the next of three
made-up readings for Paris (sunny, rain, snow), starting from one the seller picks at random. The
seller is not a realm, makes no contract call and does not know a chain exists: everything
gno-specific in [`main.go`](main.go) is a network string, one registered scheme and the page a
browser gets.

A hosted copy runs on the onyx testnet at <https://x402-demo.onyx.testnets.gno.land/weather>, at
0.25 GNOT (250000ugnot) a reading. A terminal [pays it](#paying-it) with two commands.

## Local demo

This runs a local node, the facilitator and the seller, then pays the seller. It needs Go, Node
22.18+ or 24.11+, and `gnodev` (`make install.gnodev` in a
[gnolang/gno](https://github.com/gnolang/gno) checkout).

1. From this directory, start the node, the facilitator and the seller:

   ```sh
   make demo
   ```

   It starts a node (chain id `dev`, RPC `:26657`), the facilitator on `:8402` and the seller on
   `:8401`, each once the one before it answers, and names the temporary directory that keeps
   their logs. Ctrl-C stops all three.

2. In a second terminal, pay the seller as gno's public test1 key:

   ```sh
   make pay
   ```

   It builds the client mechanism first, then prints the payment and the reading it bought:

   ```text
   buying as g1jg8mtutu9khhfwc4nxmuhcpftf0pajdhfvsqf5, gno's public test1 key; set X402_MNEMONIC to pay from your own account
   paid: tx a0a66524832960c26ad3110d998f8ad87c3ee1166ca1faac145e7e1ee9ab8b8f by g1jg8mtutu9khhfwc4nxmuhcpftf0pajdhfvsqf5
   {"celsius":24,"forecast":"sunny","location":"Paris"}
   ```

`make demo` refuses to start when something already answers at one of those addresses. The make
variables `RPC`, `FACILITATOR_LISTEN` and `LISTEN` move the node, the facilitator and the seller.
The same pieces also run one per terminal: `gnodev`, `make facilitator`, `make run`.

## Onyx testnet

Here the seller uses the public facilitator for `onyx-1`, so no local node or facilitator runs, and
the buyer pays from your own key. It needs Go and Node 22.18+ or 24.11+. Use a throwaway key, since
its mnemonic stays in your shell history. Run these from this directory.

1. Build the client mechanism the buyer imports:

   ```sh
   make -C ../.. js
   ```

2. Make a throwaway key. The command prints its mnemonic and address:

   ```sh
   node buy.mjs --new-key
   ```

3. Fund the printed address at [faucet.gno.land](https://faucet.gno.land).
4. Start the seller, with `<address>` set to the account that receives the payments:

   ```sh
   make run NETWORK=gno:onyx-1 FACILITATOR=https://facilitator.onyx.testnets.gno.land PAY_TO=<address>
   ```

5. In a second terminal, pay that seller with the key's mnemonic. `make pay` hands `RPC` to the
   buyer as `X402_GNO_RPC`, and `RPC` defaults to the local node, so set it to onyx's RPC:

   ```sh
   X402_MNEMONIC='<mnemonic>' make pay RPC=https://rpc.onyx.testnets.gno.land:443
   ```

To settle through your own facilitator instead, start it with
`make facilitator NETWORK=gno:onyx-1 RPC=https://rpc.onyx.testnets.gno.land:443`, which takes the
chain id from `NETWORK`, and leave `FACILITATOR` at its default.

## Each piece by hand

The demo is three processes, started here from the repository root. The facilitator talks to the
chain; the seller never does.

1. Start a gno node with its RPC reachable, or pick a running one. Any node works, a local dev
   chain or a testnet.
2. Start the facilitator. `-chain-id` must name the chain `-rpc` serves: the facilitator checks at
   startup and exits rather than blame payers for an operator's mismatch.

   ```sh
   go run ./cmd/gnofacilitator -rpc <RPC URL> -chain-id <chain id> -listen :8402
   ```

3. Start the seller, with `-network` set to `gno:` plus that same chain id:

   ```sh
   go run ./examples/weather \
     -pay-to g1u7y667z64x2h7vc6fmpcprgey4ck233jaww9zq \
     -network gno:<chain id> \
     -price 250000ugnot \
     -facilitator http://localhost:8402
   ```

## Seller flags

The seller takes these flags:

| Flag | Default | Meaning |
|---|---|---|
| `-pay-to` | none, required | the gno address paid for each request |
| `-network` | `gno:dev` | `gno:` plus the chain id the facilitator serves |
| `-price` | `250000ugnot` | the price per request, parsed by the chain's own coin parser, so it takes whatever `gnokey` would |
| `-facilitator` | `http://localhost:8402` | the facilitator's base URL |
| `-listen` | `:8401` | the listen address |
| `-public-url` | the address each request arrived on | the base URL buyers reach the seller at. Set it behind a TLS proxy, or every 402 names the seller's own `http://` address |
| `-rate-per-second` | `5` | sustained requests per second on the priced route, all callers together |
| `-rate-burst` | `10` | the largest spike the priced route tolerates, all callers together |

Past the rate limit, the priced route answers `429` before it checks the payment. The limit is a
backstop: throttle each client at the proxy, the only place that knows a client's address.

SIGINT or SIGTERM stops the seller once the requests in flight finish, for up to 70 s: the longest
a paid request may run. Give a deployment a stop grace period above that. A clean stop exits 0 and
a drain that runs out exits 1. A second signal during the drain is ignored; SIGKILL ends it at
once.

## What you get

Ask for the weather without paying:

```sh
curl -i http://localhost:8401/weather
```

The answer is `402`, with the terms in the `PAYMENT-REQUIRED` header as base64 JSON. v2 puts them
there, not in the body:

```json
{
  "x402Version": 2,
  "error": "Payment required",
  "resource": {
    "url": "http://localhost:8401/weather",
    "description": "Sample weather data (demo)",
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

`areFeesSponsored: false` says the payer pays the network fee inside the transaction they sign. The
mechanism sets it, so no seller writes it. There is no `paymentFlow` key because the flow is
`authorization`, the specification's default, which may be omitted.

A browser gets [`paywall.html`](paywall.html) instead, since the middleware's own paywall supports
EVM and Solana chains only. With the [Adena](https://adena.app) wallet, a visitor pays from the
page: Adena signs the bank send without broadcasting it, and [`paywall.js`](paywall.js) repeats
the request with the payment, as a stock x402 client does. Without Adena, or on a touch screen,
the page shows how to pay from code instead. `/` redirects to `/weather`, and `/favicon.ico` serves
the page's icon, unpriced.

The pay commands the page shows carry the tab's visit token, so the page can show a payment made
from a terminal. A paid request whose URL carries `?visit=<token>`, 32 hex characters, is kept for
15 minutes, up to 1000 at once, and the first payment under a token wins.
`GET /weather/visits/<token>` answers with its transaction, network, payer and the body served, or
`404`, never cached. Lookups have a rate limit of their own, 50 a second with bursts of 100 for all
pages together, whatever `-rate-per-second` and `-rate-burst` say.

## Paying it

`buy.mjs` pays the seller, and so does its Go twin [`x402-gno`](x402-gno/main.go), with the same
arguments, environment and output. Either runs from one command, without a clone. To pay from your
own Go code, register the [Go mechanism](../../client/README.md#from-go) on upstream's Go client.

> [!CAUTION]
> Each buyer signs with the mnemonic you give it, so a mnemonic that holds anything you would mind
> losing is at risk. Pay on a testnet, from a throwaway key or a
> [session key](../../client/README.md#paying-with-a-session-key).

These steps pay the hosted seller on onyx, and any other seller's URL works in its place. They
need git and Node 22.18+ or 24.11+.

1. Read [`buy.mjs`](https://github.com/gnolang/x402/blob/client/v0.5.4/examples/weather/buy.mjs),
   the buyer these commands run.
2. Make a throwaway key:

   ```sh
   npx github:gnolang/x402#client/v0.5.4 --new-key https://x402-demo.onyx.testnets.gno.land/weather
   ```

   It prints a fresh 24-word mnemonic, the address it pays from and the command that pays the
   seller with it, then exits. It sends no request and writes no file, so the printed mnemonic is
   the only copy.
3. Fund the printed address at [faucet.gno.land](https://faucet.gno.land).
4. Run the command step 2 printed. It has this shape, with the mnemonic in place of `<mnemonic>`:

   ```sh
   X402_MNEMONIC='<mnemonic>' npx github:gnolang/x402#client/v0.5.4 https://x402-demo.onyx.testnets.gno.land/weather
   ```

npx fetches the repository at that tag, builds the client mechanism and runs the root package's
`x402-gno` command, which is this `buy.mjs`. If your npm is configured with `ignore-scripts` or
`strict-allow-scripts`, it stops before the build, which is the package's `prepare` script. In that
case run the buyer from a clone, from the repository root:

```sh
make js               # build the client mechanism buy.mjs imports
node examples/weather/buy.mjs --new-key https://x402-demo.onyx.testnets.gno.land/weather
X402_MNEMONIC='<mnemonic>' node examples/weather/buy.mjs https://x402-demo.onyx.testnets.gno.land/weather
```

The Go buyer, [`x402-gno`](https://github.com/gnolang/x402/blob/v0.6.0/examples/weather/x402-gno/main.go),
takes the same steps with `go run`. It needs Go 1.26.5 or later, which Go 1.21 and later fetch by
themselves unless `GOTOOLCHAIN` is `local`. Read it first too:

```sh
go run github.com/gnolang/x402/examples/weather/x402-gno@v0.6.0 --new-key https://x402-demo.onyx.testnets.gno.land/weather
X402_MNEMONIC='<mnemonic>' go run github.com/gnolang/x402/examples/weather/x402-gno@v0.6.0 https://x402-demo.onyx.testnets.gno.land/weather
```

`go run` fetches the Go module at that release and builds the buyer, about 100 MB of modules the
first time. `go install github.com/gnolang/x402/examples/weather/x402-gno@v0.6.0` keeps it as the
`x402-gno` command. In a clone, `go run ./examples/weather/x402-gno` from the repository root runs
the checkout's buyer, and the pay command its `--new-key` prints runs from inside the clone only.

The Go buyer's pin is the Go module's tag, `vX.Y.Z`, and the npm buyer's is the client's,
`client/vX.Y.Z`: the two release on separate lines.

Each buyer registers the gno scheme on a stock x402 client, `@x402/core`'s or upstream's Go one,
and admits ugnot with a cap of 1 GNOT per request. The paying account must be funded on the chain
the offer names. Both buyers read these environment variables:

| Variable | Meaning |
|---|---|
| `X402_MNEMONIC` | the mnemonic that signs. Unset, the buyer signs as gno's public test1 key |
| `X402_SESSION_MASTER` | an account's address. `X402_MNEMONIC` is then a [session key](../../client/README.md#paying-with-a-session-key) that account registered, and the payment moves that account's coins within the session's limits |
| `X402_GNO_RPC` | the node the buyer reads its account sequence from, on the chain the offer names. Unset, it is the chain's public RPC where the buyer knows one (`onyx-1`), else `http://127.0.0.1:26657` |
| `X402_SELLER_URL` | the seller URL when the command names none. Unset, it is `http://localhost:8401/weather` |

Set `X402_GNO_RPC` for any other chain, to the node the facilitator uses.

## What happens when you pay

The seller follows [How a payment works](../../README.md#how-a-payment-works): verify, serve, then
settle. In `main.go` that shows in three places:

- **A payment the facilitator refuses gets a 402, like an unpaid request.** The buyer's 402 names
  the reason, and the middleware calls no handler for it. The seller's `OnVerifyFailure` hook logs
  it as `payment refused`, with the reason and, once the signature verified, the payer.
- **A resource that fails is never charged for.** The middleware buffers the response and skips
  settlement on any status ≥400.
- **A settlement that fails withholds the response.** The buffered body is discarded, so the cost
  of a failed settlement is the work already done rather than the data. The `ErrorHandler` is where
  a seller sees it; `SettlementHandler` is where they see the transaction that paid them.
