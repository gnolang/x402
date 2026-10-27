# AGENTS.md — working on this repo

This file is for agents **contributing to gnolang/x402**: the gno payment mechanism for
x402, its facilitator, and the harnesses that prove a payment happens. If you're here to
*use* the mechanism, the READMEs are the entry point.

## Commands

```bash
make test                # library tests. No network, no chain, no npm
make js-test             # the client mechanism, incl. the sign-doc byte-equality check, the weather page's payment logic and the weather buyer
make test-e2e            # real payments against in-memory nodes
make lint                # go vet (both modules) + gofmt
make build               # bin/gnofacilitator
make install             # the facilitator into GOPATH/bin
make js                  # install and build the client bundle
make fmt                 # gofmt the Go sources
make clean               # remove build artifacts
make help                # every target
make -C examples/weather help   # the demo: `demo` starts a node, the facilitator and the seller in one command, `pay` pays them
```

## Map

The top level is the x402 role grid — server, facilitator, client — and no directory
names a language.

| Path | What |
|---|---|
| `facilitator/` | the `/verify` `/settle` `/supported` HTTP service: upstream's facilitator engine with the gno mechanism registered, plus the rate limit, body caps, §9 reason names and the 503 that carries no verdict |
| `facilitator/exact/` | the gno mechanism: upstream's `SchemeNetworkFacilitator`, verification, settlement and the reason vocabulary. `exacttest/` holds the fixtures both packages' tests share |
| `server/exact/` | upstream's `SchemeNetworkServer` implemented for gno (package `exact`) |
| `client/src/` | upstream's `SchemeNetworkClient`, TypeScript; emits to `client/dist/` |
| `client/exact/` | upstream's `SchemeNetworkClient`, Go. It signs the payment the TypeScript client signs: the same transaction under the same signature bytes, held to it by payments that client signed, committed in `testdata/` (`go generate ./client/exact` signs them again, with npm). For a payer's own key the bytes sent differ, because amino writes the zero `session_addr` the TypeScript encoding leaves out. `keybase/` signs with a key gnokey keeps, in a package of its own so `client/exact` links no keybase or Ledger code |
| `cmd/gnofacilitator/` | the binary |
| `examples/weather/` | the demo: a priced endpoint (`main.go`), the page a browser gets and pays through with Adena (`paywall.html`, `paywall.js`, tested by `make js-test`), the buyer that pays it from a script (`buy.mjs`, also the root package's `x402-gno` command) and its Go twin (`x402-gno/`, the same arguments, environment and output), and a Makefile that runs the seller, `buy.mjs` and a facilitator |
| `e2e/` | real payments through real nodes — its own Go module. `buyer.mjs` beside it is the buyer the contract scenarios drive; the happy path, the session payment, the pending settlement, the refused one, the duplicate one and the unfunded payer drive `examples/weather/buy.mjs`, and `weatherbuyer_test.go` runs them again with `examples/weather/x402-gno`, which needs no npm. `goclient_test.go` drives upstream's Go client with `client/exact` registered through the scenarios in `testdata/goclient/`, which need no npm. `settle_test.go` settles one payment twice at once through two facilitators on one node, two on two nodes and one over two nodes, and replays a settled one |
| `docs/releasing.md` | the three independent release lines: facilitator, client, weather image |
| `docs/deploying.md` | running the facilitator image: flags, endpoints, limits, failure modes |

`e2e` is a separate Go module on purpose, so `go test ./...` cannot start a chain by
accident and needs no build tag to stay out of the way.

## Conventions

- TDD: failing test first. Unit tests are Go-native; the payment claim is proved by the
  e2e, not by mocks.
- testify `require`/`assert`; small focused test funcs over mega-tables.
- Conventional commits. Scopes follow the top-level directories — `facilitator`,
  `server`, `client`, `e2e`, `examples`. A release bump is
  `chore(release): bump the client to X.Y.Z`.
- Comments say WHAT and WHY, never what changed. A reader must understand without the
  history.
- `package.json` carries its reasoning in `//`-prefixed sibling keys (`//exports`,
  `//scripts`, `//publishConfig`, …). Keep that idiom rather than dropping a bare field
  whose purpose isn't obvious.
- `gofmt` runs over `.`, not a named tree, so a new Go directory is covered without
  editing anything.
- `e2e/buyer.mjs` imports the client as `@gnolang/x402/exact/client`, through the
  manifest's `exports` map, so the payment test exercises the map. Keep that import; never
  point it at a built path.

## Security invariants — never break

- **The facilitator holds no key.** It verifies offline and broadcasts a transaction the
  payer already signed, so it cannot move funds anywhere the payer did not sign for.
- **A scheme payload is relayed verbatim.** The transport hands the engine the payload's
  bytes as received, and what is broadcast is the transaction decoded from the payer's own
  base64; never re-encode a `transaction` on the way through.
- **Everything decidable offline is decided before the node is touched**, so a payment
  that cannot settle costs the facilitator nothing.
- **A payment settles once per facilitator process.** `exact.Facilitator` claims a
  payment's transaction hash before checking it and keeps it for five minutes after a block
  executes it; a copy is refused as `duplicate_settlement`. Without the claim, two nodes
  each admit a copy and answer both broadcasts with the one delivery: one payment, two
  resources.
- **A reason code names something established about the payment.** A facilitator's own
  outage is not that: when the chain did not answer, or did not answer the broadcast,
  return `503` with no verdict. Once a node admitted the payment, answer for it: success or
  the chain's refusal when a block executed it, `settlement_pending` with its hash until
  then. A false failure tells a seller to discard a response the buyer paid for, and a
  `503` after the node admitted the payment leaves the buyer without the hash.
- **The reason vocabulary is pinned by `TestReasonVocabulary`**, and the v2 header
  contract by the `TestConformance_*` tests, transcribed from the specification. Adding
  or renaming a code means updating them in the same change.
- **No npm token exists in this repository.** Publishing is OIDC trusted publishing; see
  `docs/releasing.md`.

## Housekeeping — what to update when

| When you… | Update |
|---|---|
| Add or rename a reason code | `facilitator/exact/verify.go` · `TestReasonVocabulary` |
| Change the payload | `facilitator/exact` (`transactionOf`, `VerifyStatic`) · the client's payload builder in `client/src/exact/client.ts` — both halves sign the same bytes · the Go client's in `client/exact/scheme.go`, and its fixtures with `go generate ./client/exact`, since it is held to the TypeScript client's signatures · the weather page's `payWithAdena` in `examples/weather/paywall.js`, which builds the same envelope around what Adena signs |
| Rename `.github/workflows/release.yml` | the npm **trusted publisher** on npmjs.com, which matches the filename exactly. Publishing breaks until it does |
| Rename the client job's `npm` environment | the npm **trusted publisher** on npmjs.com, which matches the environment exactly · a GitHub environment of the new name with the `main` branch rule, as `docs/releasing.md` (Credentials) describes. Publishing breaks until both match |
| Change the release flow or a tag scheme | `docs/releasing.md` |
| Bump the Go toolchain | `go-version` in `.github/workflows/*.yml` · the `golang` image pin in `docker/weather.Dockerfile` · the Go version the `x402-gno` commands need, in `examples/weather/paywall.html` and `examples/weather/README.md` |
| Change a flag, default, timeout, limit or exit code in `cmd/gnofacilitator` | `docs/deploying.md` |
| Change a flag, default or limit of the weather seller (`examples/weather/*.go`) | `examples/weather/README.md` (Seller flags, and the visit lookup limits in What you get) |
| Follow the target testnet to a new `gnolang/gno` chain tag | `github.com/gnolang/gno` in `go.mod` and `e2e/go.mod` · re-record `facilitator/exact/testdata/onyx_simulate_send.json` |
| Bump `@gnolang/gno-js-client` or `@gnolang/tm2-js-client` | regenerate the Go client's fixtures with `go generate ./client/exact` and run its tests: nothing in CI does, so the two clients could drift with `npm test` still green |
| Release the client | bump `package.json`'s version — the workflow refuses a dispatch that disagrees with it. The facilitator's version is its git tag alone, so it needs no bump. Bump the `client/vX.Y.Z` the `npx github:gnolang/x402#…` commands pin in `examples/weather/paywall.html`, `examples/weather/README.md` and `client/README.md`, and the tag the `buy.mjs` links on the weather page and in its README name, in the same change; `buy.mjs --new-key` reads it from the version |
| Release the facilitator | bump the `vX.Y.Z` the `go run`/`go install github.com/gnolang/x402/examples/weather/x402-gno@…` commands pin in `examples/weather/paywall.html`, `examples/weather/README.md` and `client/README.md`, and the tag the `x402-gno` links on the weather page and in its README name, in a change merged before the release; `x402-gno --new-key` reads it from the module version it was built from |
| Change what `buy.mjs` or `examples/weather/x402-gno` prints, or the arguments and environment it reads | the other one, in the same change: the weather page offers either, and the e2e scenarios hold both to the same output · the environment table in `examples/weather/README.md` (Paying it) and the sample output in its Local demo |
| Add a make target | this file (Commands) |
| Import a new package in `examples/weather/buy.mjs` | declare it in `dependencies` (exact version) or `peerDependencies`. The buyer runs as the package's `x402-gno` command, where devDependencies are not installed; `buy.test.js` fails otherwise |

Stale docs are bugs: if a change makes a claim in a README or in `docs/` wrong, fixing it
is part of the change, not a follow-up.

## Special files

- `CLAUDE.md` is a **symlink to this file** — one brief, no second copy to drift.
