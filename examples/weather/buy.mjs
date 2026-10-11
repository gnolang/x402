// Buys the weather from the seller in this directory: one paid GET, printed for
// a human. The x402 flow — recognising the 402, decoding the offer, signing,
// retrying — is a stock @x402/fetch client; the gno-aware lines are the
// register() call below and the spend control that admits ugnot.
//
//   node buy.mjs [seller URL]
//
// The buyer signs with the mnemonic in X402_MNEMONIC — a funded account on the
// chain the seller's offer names — and reads its account sequence from the chain,
// so X402_GNO_RPC must serve that same chain.
import { GnoJSONRPCProvider, GnoWallet } from "@gnolang/gno-js-client";
import { decodePaymentRequiredHeader } from "@x402/core/http";
import { decodePaymentResponseHeader, wrapFetchWithPayment, x402Client } from "@x402/fetch";

import { ExactGnoScheme } from "@gnoverse/x402-gno/exact/client";

// gno's well-known test1 mnemonic, the default for a local test chain.
const TEST1 =
  "source bonus chronic canvas draft south burst lottery vacant surface solve popular case indicate oppose farm nothing bullet exhibit title speed wink action roast";

const mnemonic = process.env.X402_MNEMONIC || TEST1;

// The most this buyer pays for one request, in ugnot. The network fee is paid on
// top of it.
const MAX_PRICE = "1000000";

const sellerURL = process.argv[2] ?? process.env.X402_SELLER_URL ?? "http://localhost:8401/weather";
// The node reports its address in tm2's own form, which names the transport
// rather than the scheme an HTTP client needs.
const rpcURL = (process.env.X402_GNO_RPC ?? "http://127.0.0.1:26657").replace(
  /^tcp:\/\//,
  "http://",
);

const wallet = await GnoWallet.fromMnemonic(mnemonic);
wallet.connect(await GnoJSONRPCProvider.create(rpcURL));

// A stock client only pays in USD-pegged assets until told otherwise, and ugnot
// is not one.
const client = new x402Client()
  .register("gno:*", new ExactGnoScheme(wallet))
  .setSpendControls({
    allowedAssets: [{ network: "gno:*", asset: "ugnot", maxAmountPerPayment: MAX_PRICE }],
  });
const fetchWithPayment = wrapFetchWithPayment(fetch, client);

console.log(`buying as ${await wallet.getAddress()}`);

const paid = await fetchWithPayment(sellerURL);
const body = await paid.text();

if (!paid.ok) {
  // A refusal names its reason in the challenge that answers it.
  const challenge = paid.headers.get("PAYMENT-REQUIRED");
  const reason = challenge ? (decodePaymentRequiredHeader(challenge).error ?? "") : "";
  console.log(`not paid (status ${paid.status})${reason ? `: ${reason}` : ""}`);
  process.exit(1);
}

const receipt = decodePaymentResponseHeader(paid.headers.get("PAYMENT-RESPONSE"));
console.log(`paid: tx ${receipt.transaction}`);
console.log(body.trim());
