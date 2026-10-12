# The gno client mechanism

Pay a gno.land chain with x402, from a stock client — no fork, no patch, no upstream change.

Install it with `npm i @gnoverse/x402-gno`, or build it from the repository root with `make js`.
[`examples/weather/buy.mjs`](../examples/weather/buy.mjs) is a working buyer.

```js
import { x402Client } from "@x402/core/client";
import { wrapFetchWithPayment } from "@x402/fetch";
import { ExactGnoScheme } from "@gnoverse/x402-gno/exact/client";

const client = new x402Client();
client.register("gno:*", new ExactGnoScheme(wallet)); // ← teaches the client gno
client.setSpendControls({
  allowedAssets: [{ network: "gno:*", asset: "ugnot", maxAmountPerPayment: "1000000" }],
});

const paid = wrapFetchWithPayment(fetch, client);
const res = await paid("https://api.example.com/weather");
```

`wallet` is a `GnoWallet` from `@gnolang/gno-js-client`, connected to a provider. The connection is
required: a gno sequence is sequential, so only the chain knows the next one.

`setSpendControls` admits ugnot. It exists from `@x402/core` 2.23, and from that version a stock
client pays only in USD-pegged assets unless they are listed there; ugnot is not one.
`maxAmountPerPayment` is the most one request may cost, in ugnot; the network fee is paid on top.
The mechanism signs for any gno denomination, so an offer priced in another one is paid once an
entry lists it. The fee stays in ugnot whatever the price is in.

`e2e/buyer.mjs` uses that same package-name import from inside this repository, resolving through the
manifest's `exports` map rather than a built path — so the map is exercised by the payment test
rather than only by consumers.

## What it is

`ExactGnoScheme` implements `SchemeNetworkClient` from `@x402/core` — a scheme name plus one method.
It does nothing else: no HTTP, no 402 handling, no broadcasting. The client library owns the
protocol, the facilitator owns settlement, and this owns only the chain-specific step between them.

The subpath is the ecosystem's grid: `@x402/evm` publishes `./exact/client`, `./exact/server` and
`./exact/facilitator`, so a buyer that already imports one reaches the same way for this. gno's
server and facilitator halves are Go, so `./exact/client` is the only cell this package fills.

Its payload is one field, `transaction`: base64 of a fully signed, unbroadcast `std.Tx` carrying a
single `bank.MsgSend`.

## The chain id comes from the offer

The payment is signed against the chain id the **offer** names, not the one the wallet's node
reports. That is worth stating because the obvious implementation cannot do it:
`Wallet.signTransaction` takes the chain id from the node it is connected to and offers no override.
This builds the sign doc itself instead, and a test holds the result byte-identical to the wallet's
own signing.

The sign doc renders the fee in the shape of gnolang/gno#6173, as `@gnolang/tm2-js-client` 3 does.
A chain without that change refuses the signature; onyx-1 has it.

The chain id is the only sign-doc field that comes from the offer. The account number and the
sequence still come from the wallet's provider, because only a chain knows the next sequence for an
account — so paying a second gno chain also means pointing the wallet at a node on it. A wallet
connected elsewhere signs over that node's account state, and the facilitator verifying against the
paid chain refuses the result as `invalid_exact_gno_payload_signature_invalid`.

## The fee is yours

gno has no fee delegation and this scheme does not pretend otherwise. `std.Fee` names no payer and
the chain charges the first signer, so the payer of the payment is also the payer of the network
fee, and the facilitator holds no key — it only broadcasts what you signed. A gno offer says so:
`extra.areFeesSponsored` is `false`.

The requirements carry no gas fields, so the buyer chooses. The chain takes the whole fee offered,
not the gas used, so the fee follows the chain's own gas price, read from the wallet's node: twice
that price for 10,000,000 gas, rounded up. The price adapts to load, and an underpriced payment is
refused at settle and costs you the resource, hence the margin. On onyx-1, at 1ugnot per 1000 gas,
that is 20000ugnot. A block above the chain's gas target raises the price's amount by at least 1, so
on onyx-1 one such block between signing and settle doubles it, and the margin covers exactly one. A
chain that reports no gas price gets 1000000ugnot, clear of any minimum a node sets on its own.

## Paying with a session key

An agent can pay with a session key instead of its owner's key. A session is a key the owner
registers on chain under their own account. The chain enforces its limits whatever the agent's
code does: the messages it may sign, a spend limit per period, and an expiry. A session limited to
`bank/send` can pay any x402 seller and do nothing else.

The agent makes a fresh key and hands the owner its public key, the `pub:` field `gnokey list`
prints for it. The owner registers it, here for 5 GNOT a day (`-spend-period` is in seconds) over
a week:

```sh
gnokey maketx session create \
  -pubkey gpub1... \
  -allow-paths bank/send \
  -spend-limit 5000000ugnot -spend-period 86400 \
  -expires-at 7d \
  -gas-fee 4000ugnot -gas-wanted 2000000 -broadcast \
  -chainid onyx-1 -remote https://rpc.onyx.testnets.gno.land:443 \
  <owner key>
```

It uses about 1,360,000 gas on onyx-1, and the fee is twice the chain's price for the gas it
reserves.

The agent's wallet holds the session key, and the owner's address goes in `master`:

```js
const wallet = await GnoWallet.fromMnemonic(sessionMnemonic);
wallet.connect(await GnoJSONRPCProvider.create(rpcURL));
client.register("gno:*", new ExactGnoScheme(wallet, { master: "g1owner..." }));
```

Payments move the owner's coins, and the settlement names the owner as the payer. The session key
holds no coins and is never an account of its own: the chain refuses to register a key whose
address already holds an account, so make a fresh one. The wallet's provider must be a gno
provider, such as `GnoJSONRPCProvider`: the signature covers the number and sequence the chain
keeps for the session under the owner, and only a gno provider reads them.

- **Fees count against the spend limit.** A payment spends its price plus its fee, 20000ugnot on
  onyx-1.
- **A payment over the limit is refused at verify.** The facilitator's simulation fails it as
  `invalid_exact_gno_transaction_simulation_failed`, and nothing is broadcast.
- **The limit caps an amount per period, not a set of payees.** `bank/send` names no recipient.
  `maxAmountPerPayment` still caps each payment on the client's side.
- **Each session has its own sequence.** Payments signed by different sessions of one owner do not
  race each other, so use one session per concurrent stream of payments.
- **Revoke a session** with `gnokey maketx session revoke -pubkey gpub1... <owner key>` and the
  same fee, chain and remote flags. A revoked session's payments fail before they are signed.

## What it refuses

The requirements arrive from a seller over the network, so they are validated rather than trusted —
a malformed amount or a missing `payTo` would otherwise become a signed transaction that pays the
wrong thing. It throws on a network that is not `gno:<chain-id>`, a scheme that is not `exact`, an
asset that is not a gno denomination, an amount that is not a positive integer, an absent `payTo`, an
`extra.memo` that is present but not a string, an `extra.assetTransferMethod` other than
`bank-send`, and an `extra.paymentFlow` other than `authorization`. A null transfer method or flow
names nothing, as upstream's SDKs read it.

## Development

Sources are here in `client/src`, laid out by the subpath they publish, and the emit lands beside
them; the manifest is at the repository root, so these run from there.

```sh
npm run typecheck   # TypeScript 7 (the native compiler)
npm test            # vitest
npm run build       # typecheck, then emit client/dist/
```
