// Buys the weather from the seller in this directory: one paid GET, printed for
// a human. The x402 flow is the five numbered steps below: ask, read the terms
// from the 402, sign, ask again with the payment, read the receipt. @x402/core's
// stock client encodes and decodes each message; the gno-aware lines are the
// register() call and the spend control that admits ugnot.
//
//   node buy.mjs [seller URL]
//
// The buyer signs with the mnemonic in X402_MNEMONIC — a funded account on the
// chain the seller's offer names — and reads its account sequence from the chain,
// so X402_GNO_RPC must serve that same chain. With X402_SESSION_MASTER set to an
// account's address, X402_MNEMONIC is instead a session key that account
// registered, and payments move that account's coins.
import { GnoJSONRPCProvider, GnoWallet } from "@gnolang/gno-js-client";
import { x402Client, x402HTTPClient } from "@x402/core/client";
import { decodePaymentRequiredHeader, decodePaymentResponseHeader } from "@x402/core/http";

import { ExactGnoScheme } from "@gnoverse/x402-gno/exact/client";

// gno's well-known test1 mnemonic, the default for a local test chain.
const TEST1 =
  "source bonus chronic canvas draft south burst lottery vacant surface solve popular case indicate oppose farm nothing bullet exhibit title speed wink action roast";

const mnemonic = process.env.X402_MNEMONIC || TEST1;
const master = process.env.X402_SESSION_MASTER || undefined;

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
  .register("gno:*", new ExactGnoScheme(wallet, { master }))
  .setSpendControls({
    allowedAssets: [{ network: "gno:*", asset: "ugnot", maxAmountPerPayment: MAX_PRICE }],
  });
const http = new x402HTTPClient(client);

const address = await wallet.getAddress();
if (master !== undefined) {
  console.log(`buying as ${master}, signed by its session key ${address}`);
} else if (mnemonic === TEST1) {
  console.log(`buying as ${address}, gno's public test1 key; set X402_MNEMONIC to pay from your own account`);
} else {
  console.log(`buying as ${address}`);
}

let paid;
try {
  // 1. Ask without paying. A priced resource answers 402; any other answer is
  // the final one.
  paid = await fetch(sellerURL);
  if (paid.status === 402) {
    const unpaid = paid;
    // 2. Read the terms, which the 402 carries in its PAYMENT-REQUIRED header.
    const terms = http.getPaymentRequiredResponse((name) => unpaid.headers.get(name));
    // 3. Sign a payment that meets them: a bank send of the price to the payee,
    // within MAX_PRICE. Nothing is broadcast here.
    const payment = await client.createPaymentPayload(terms);
    // 4. Ask again with the signed payment in PAYMENT-SIGNATURE. The seller has
    // its facilitator check it and settle it on chain before answering. The
    // second header is one @x402/fetch adds to every payment, so a seller gets
    // the same request from this buyer as from that client.
    paid = await fetch(sellerURL, {
      headers: {
        ...http.encodePaymentSignatureHeader(payment),
        "Access-Control-Expose-Headers": "PAYMENT-RESPONSE,X-PAYMENT-RESPONSE",
      },
    });
  }
} catch (err) {
  // Thrown when the seller cannot be reached or no payment can be signed, such as
  // for an account the chain has never funded.
  notPaid(err);
}

// 5. Read the answer: the weather once the payment settled, and the receipt in
// PAYMENT-RESPONSE.
const body = await paid.text();

if (!paid.ok) {
  // A verify refusal carries its reason in the challenge, a settle failure in the
  // receipt.
  const receiptHeader = paid.headers.get("PAYMENT-RESPONSE");
  const challenge = paid.headers.get("PAYMENT-REQUIRED");
  const receipt = receiptHeader ? decodePaymentResponseHeader(receiptHeader) : {};
  const reason = (receipt.errorReason || (challenge && decodePaymentRequiredHeader(challenge).error) || "").trim();
  if (receipt.errorReason === "settlement_pending") {
    // The chain admitted the payment and has not executed it yet: the weather is
    // withheld, but the payment may still go through, and the hash is how to
    // find out.
    console.log(
      `not confirmed yet: tx ${receipt.transaction} (${reason}); the weather was withheld, and the payment may still go through`,
    );
  } else {
    // A refusal may name a transaction as well, such as one a block executed
    // and aborted, which still charged the network fee.
    const tx = receipt.transaction ? `; tx ${receipt.transaction}` : "";
    console.log(`not paid (status ${paid.status})${reason ? `: ${reason}` : ""}${tx}`);
  }
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
