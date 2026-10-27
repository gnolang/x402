# The gno client mechanism

Pay a gno.land chain with x402 from a stock client: no fork, no patch, no upstream change.
`ExactGnoScheme` implements upstream's `SchemeNetworkClient`, in TypeScript here and in Go in
[`client/exact`](exact). It builds and signs the payment and does nothing else: the client library
owns the 402, the headers and the retry, and the facilitator owns settlement.

The payload is one field, `transaction`: base64 of a fully signed, unbroadcast `std.Tx` carrying a
single `bank.MsgSend`.

## From JavaScript

Install it with `npm i @gnolang/x402`, or build it from the repository root with `make js`.
Register it on a stock client and admit ugnot to the client's spend controls:

```js
import { x402Client } from "@x402/core/client";
import { wrapFetchWithPayment } from "@x402/fetch";
import { ExactGnoScheme } from "@gnolang/x402/exact/client";

const client = new x402Client();
client.register("gno:*", new ExactGnoScheme(wallet)); // ← teaches the client gno
client.setSpendControls({
  allowedAssets: [{ network: "gno:*", asset: "ugnot", maxAmountPerPayment: "1000000" }],
});

const paid = wrapFetchWithPayment(fetch, client);
const res = await paid("https://api.example.com/weather");
```

`wallet` is a `GnoWallet` from `@gnolang/gno-js-client`, connected to a provider. The connection is
required: a gno sequence is sequential, so only the chain knows the next one. Each read before
signing gets up to three tries of 10 s each, whatever the failure, so a refusal, such as an account
the chain has never seen, is thrown after the third. Nothing is retried once a payment is signed.

`setSpendControls` exists from `@x402/core` 2.23. From that version a stock client pays only in
USD-pegged assets unless they are listed there, and ugnot is not one. `maxAmountPerPayment` is the
most one request may cost, in ugnot; the network fee is paid on top. The mechanism signs for any gno
denomination, so an offer priced in another one is paid once an entry lists it. The fee stays in
ugnot whatever the price is in.

[`examples/weather/buy.mjs`](../examples/weather/buy.mjs) is a working buyer, and the package
installs it as the `x402-gno` command: `npx github:gnolang/x402#client/v0.5.4 <seller URL>` runs it
without a clone. It signs with the mnemonic it is given, so read it first;
[Paying it](../examples/weather/README.md#paying-it) shows how to make a throwaway key with it.

## From Go

[`client/exact`](exact) is a package of this repository's Go module, registered on upstream's Go
client:

```go
import (
	"net/http"
	"time"

	"github.com/gnolang/gno/tm2/pkg/crypto/keys"
	x402 "github.com/x402-foundation/x402/go/v2"
	x402http "github.com/x402-foundation/x402/go/v2/http"

	clientexact "github.com/gnolang/x402/client/exact"
)

kb, _ := keys.NewKeyBaseFromDir(gnokeyHome) // the keys gnokey keeps
signer, _ := clientexact.KeybaseSigner(kb, "buyer", password)
node, _ := clientexact.NewRPCNode("https://rpc.onyx.testnets.gno.land:443")

client := x402.Newx402Client().
	Register("gno:onyx-1", clientexact.NewExactGnoScheme(signer, node)). // ← teaches the client gno
	SetSpendControls(x402.SpendControls{AllowedAssets: []x402.SpendControlAsset{
		{Network: "gno:onyx-1", Asset: "ugnot", MaxAmountPerPayment: "1000000"},
	}})

paid := x402http.WrapHTTPClientWithPayment(&http.Client{Timeout: time.Minute}, x402http.Newx402HTTPClient(client))
res, err := paid.Get("https://x402-demo.onyx.testnets.gno.land/weather")
```

[`examples/weather/x402-gno`](../examples/weather/x402-gno/main.go) is a working buyer built on
it: `go run github.com/gnolang/x402/examples/weather/x402-gno@v0.6.0 <seller URL>` runs it without
a clone. It too signs with the mnemonic it is given, so read it first.

The Go and JavaScript mechanisms sign the same transaction with the same signature bytes for the
same offer, key and chain state, and a test holds them to it. The encoded bytes sent can still
differ: for a payer's own key, amino writes the zero `session_addr` the JavaScript encoding leaves
out. The one exception to matching signatures is a memo holding U+2028 or U+2029: tm2's sign doc
escapes those and the JavaScript one does not, so the chain refuses the JavaScript signature over
such a memo. The Go mechanism differs in these ways:

- **The signer is any key.** `Signer` is a public key and a sign method, so a `crypto.PrivKey` is
  one, and `KeybaseSigner` adapts a key gnokey keeps, a Ledger key included.
- **The key can sign somewhere else.** `clientexact.Prepare` builds and checks a payment without
  signing it, so a buyer can show a person the price first, or have gnokey or a device sign.
  `Payment.Payload` verifies that signature, over either rendering of the sign doc the chain
  accepts and by the key the chain will check it against, and returns the scheme payload; the
  `PaymentPayload` sent to a seller wraps it with the accepted requirements, as upstream's client
  does.
- **The node must serve the offer's chain.** A payment signs over the account number and sequence
  its node reports, so an offer for any other chain is refused before anything is signed. Register
  one scheme per gno network, each with a node on that network, rather than one under `gno:*`.
- **Reads are not retried.** Each is bounded by 60 s and by its context, an answer over 1 MiB is
  refused, and nothing is signed until all have answered.
- **The spend cap holds in the scheme too.** Upstream's Go client hands the scheme the cap its
  spend controls resolved for the asset, and a price above it is refused unsigned, even when a
  payment is asked for directly rather than chosen from a 402.
- **The fee has a ceiling.** The fee follows the gas price the buyer's node reports, and a node
  can report any price, while neither the spend cap nor the facilitator bounds the fee. A fee
  above 1 GNOT, or in a coin other than ugnot, is refused as `ErrExcessiveFee`.
- **A payment its caller gave up on is not signed.** Every read takes the caller's context, and
  the context is checked once more before the signer is asked.

## The chain id comes from the offer

The JavaScript mechanism signs against the chain id the **offer** names, not the one the wallet's
node reports. `Wallet.signTransaction` takes the chain id from its node and offers no override, so
the mechanism builds the sign doc itself, and a test holds the result byte-identical to the
wallet's own signing. The sign doc renders the fee in the shape of gnolang/gno#6173, as
`@gnolang/tm2-js-client` 3 does. A chain without that change refuses the signature; onyx-1 has it.

The account number and the sequence still come from the wallet's provider, because only a chain
knows an account's next sequence. Paying a second gno chain therefore means pointing the wallet at
a node on it. A wallet connected elsewhere signs over that node's account state, and the
facilitator verifying against the paid chain refuses the result as
`invalid_exact_gno_payload_signature_invalid`.

The Go mechanism passes the chain id to tm2's `std.Tx.GetSignBytes`, so it needs no workaround. It
asks its node which chain it serves and refuses an offer for any other before signing.

## The fee is yours

The payer pays the network fee: gno has no fee delegation, and the facilitator only broadcasts what
the payer signed. The requirements carry no gas fields, so the buyer chooses the fee. The chain
takes the whole fee offered, not the gas used. The fee therefore follows the chain's own gas price,
read from the wallet's node: twice that price for 10,000,000 gas, rounded up. On onyx-1, at 1ugnot
per 1000 gas, that is 20000ugnot.

Twice the price is a margin, because the price adapts to load, and an underpriced payment is
refused at settle and costs the buyer the resource. A block above the chain's gas target raises
the price's amount by at least 1, so on onyx-1 one such block between signing and settle doubles
it, and the margin covers exactly one. A chain that reports no gas price gets 1000000ugnot, clear of any
minimum a node sets on its own.

## Paying with a session key

An agent can pay with a session key instead of its owner's key. A session is a key the owner
registers on chain under their own account, and the chain enforces its limits whatever the agent's
code does: the messages it may sign, a spend limit per period, and an expiry. A session limited to
`bank/send` can pay any x402 seller and do nothing else.

1. The agent makes a fresh key and hands the owner its public key, the `pub:` field `gnokey list`
   prints for it. The key must be new: the chain refuses to register a key whose address already
   holds an account.
2. The owner registers it. The command below lets it spend 5 GNOT a day (`-spend-period` is in
   seconds) for a week:

   ```sh
   gnokey maketx session create \
     -pubkey <session public key> \
     -allow-paths bank/send \
     -spend-limit 5000000ugnot -spend-period 86400 \
     -expires-at 7d \
     -gas-fee 4000ugnot -gas-wanted 2000000 -broadcast \
     -chainid onyx-1 -remote https://rpc.onyx.testnets.gno.land:443 \
     <owner key>
   ```

   `<session public key>` is the `pub:` field from step 1, and `<owner key>` the name of the
   owner's key in gnokey. It uses about 1,360,000 gas on onyx-1, and the fee is twice the chain's
   price for the gas it reserves.
3. The agent's wallet holds the session key, and the owner's address goes in `master`:

   ```js
   const wallet = await GnoWallet.fromMnemonic(sessionMnemonic);
   wallet.connect(await GnoJSONRPCProvider.create(rpcURL));
   client.register("gno:*", new ExactGnoScheme(wallet, { master: "<owner address>" }));
   ```

   From Go, the session key is the scheme's signer and the owner's address an option:

   ```go
   scheme := clientexact.NewExactGnoScheme(sessionSigner, node, clientexact.WithMaster(owner))
   ```

Payments move the owner's coins, and the settlement names the owner as the payer. The session key
holds no coins and is never an account of its own. The wallet's provider must be a gno provider,
such as `GnoJSONRPCProvider`: the signature covers the number and sequence the chain keeps for the
session under the owner, and only a gno provider reads them.

- **Fees count against the spend limit.** A payment spends its price plus its fee, and the fee is
  20000ugnot on onyx-1.
- **A payment over the limit is refused at verify.** The facilitator's simulation fails it as
  `invalid_exact_gno_transaction_simulation_failed`, and nothing is broadcast.
- **The limit caps an amount per period, not a set of payees.** `bank/send` names no recipient.
  `maxAmountPerPayment` still caps each payment on the client's side.
- **Each session has its own sequence.** Payments signed by different sessions of one owner do not
  race each other, so use one session per concurrent stream of payments.
- **Revoke a session** with
  `gnokey maketx session revoke -pubkey <session public key> <owner key>` and the same fee, chain
  and remote flags. A revoked session's payments fail before they are signed.

## What it refuses

The requirements arrive from a seller over the network, so both mechanisms validate them rather
than trust them: a malformed amount or a missing `payTo` would otherwise become a signed
transaction that pays the wrong thing. Both refuse:

- a network that is not `gno:<chain-id>`;
- a scheme that is not `exact`;
- an asset that is not a gno denomination;
- an amount that is not a positive integer;
- an absent `payTo`;
- an `extra.memo` that is present but not a string, or longer than 256 bytes;
- an `extra.assetTransferMethod` other than `bank-send`;
- an `extra.paymentFlow` other than `authorization`.

A null transfer method or flow names nothing, as upstream's SDKs read it.

The Go mechanism wraps those in `ErrInvalidRequirements`, and also refuses:

- a `payTo` not written as the canonical bech32 address the facilitator compares the recipient
  with, or the zero address;
- a chain id CAIP-2 cannot name, such as the pattern `gno:*`, which no facilitator can serve;
- an amount past what one coin holds;
- a price above the spend cap, as `ErrExceedsSpendCap`, and a fee above 1 GNOT, as
  `ErrExcessiveFee`;
- a multisig key, and an offline one in `KeybaseSigner`, since neither can make a payment that
  settles. These two refuse the buyer's own key rather than the offer, so they do not wrap
  `ErrInvalidRequirements`.

## Development

The TypeScript sources are in `client/src`, laid out by the subpath they publish, and the build
emits to `client/dist/` beside them. The manifest is at the repository root, so these run from
there:

```sh
npm run typecheck   # TypeScript 7 (the native compiler)
npm test            # vitest
npm run build       # typecheck, then emit client/dist/
```

`make test` runs the Go mechanism's tests with the rest of the module. They hold it to payments the
JavaScript mechanism signed, committed under `client/exact/testdata`. `go generate ./client/exact`
signs those again. It needs npm, and the only reasons to run it are a change to what either
mechanism signs and a bump of `@gnolang/gno-js-client` or `@gnolang/tm2-js-client`.
