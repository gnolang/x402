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

// Prints why nothing was paid, then exits. fetch reports every network failure
// as "fetch failed" and keeps the reason in err.cause.
function notPaid(err) {
  const message = err instanceof Error ? err.message : String(err);
  const cause = err instanceof Error && err.cause instanceof Error ? ` (${err.cause.message})` : "";
  console.log(`not paid: ${message}${cause}`);
  process.exit(1);
}

const wallet = await GnoWallet.fromMnemonic(mnemonic);
try {
  wallet.connect(await GnoJSONRPCProvider.create(rpcURL));
} catch (err) {
  notPaid(err);
}

// A stock client only pays in USD-pegged assets until told otherwise, and ugnot
// is not one.
const client = new x402Client()
  .register("gno:*", new ExactGnoScheme(wallet))
  .setSpendControls({
    allowedAssets: [{ network: "gno:*", asset: "ugnot", maxAmountPerPayment: MAX_PRICE }],
  });
const fetchWithPayment = wrapFetchWithPayment(fetch, client);

const address = await wallet.getAddress();
console.log(
  mnemonic === TEST1
    ? `buying as ${address}, gno's public test1 key; set X402_MNEMONIC to pay from your own account`
    : `buying as ${address}`,
);

let paid;
try {
  paid = await fetchWithPayment(sellerURL);
} catch (err) {
  // Thrown when the seller cannot be reached or no payment can be signed, such as
  // for an account the chain has never funded.
  notPaid(err);
}
const body = await paid.text();

if (!paid.ok) {
  // A verify refusal carries its reason in the challenge, a settle failure in the
  // receipt.
  const receipt = paid.headers.get("PAYMENT-RESPONSE");
  const challenge = paid.headers.get("PAYMENT-REQUIRED");
  const reason = (
    (receipt && decodePaymentResponseHeader(receipt).errorReason) ||
    (challenge && decodePaymentRequiredHeader(challenge).error) ||
    ""
  ).trim();
  console.log(`not paid (status ${paid.status})${reason ? `: ${reason}` : ""}`);
  process.exit(1);
}

const settlement = paid.headers.get("PAYMENT-RESPONSE");
if (settlement === null || settlement === "") {
  console.log("served without payment");
} else {
  const receipt = decodePaymentResponseHeader(settlement);
  console.log(`paid: tx ${receipt.transaction} by ${receipt.payer}`);
}
console.log(body.trim());
