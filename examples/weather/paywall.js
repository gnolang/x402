// The browser half of the weather seller's 402 page: it pays the resource with
// the Adena wallet the way a stock x402 client does, and shows the live terms.
// The seller inlines this file into the page, so it stays one self-contained
// module: no imports, no build step, no request beyond the seller itself.

const NETWORK_PREFIX = "gno:";

// tm2's denomination grammar, as the client mechanism checks it: the coin sent
// is amount + asset as one string, so an asset outside it signs a different
// coin.
const DENOMINATION = /^[a-z/][a-z0-9_.:/-]{2,273}$/;
const AMOUNT = /^[1-9][0-9]*$/;

/** Encodes value as an x402 header carries it: base64 of its JSON, UTF-8. */
export function encodeHeader(value) {
  const bytes = new TextEncoder().encode(JSON.stringify(value));
  return btoa(Array.from(bytes, (b) => String.fromCharCode(b)).join(""));
}

/** Decodes an x402 header back to its JSON value. */
export function decodeHeader(header) {
  const bytes = Uint8Array.from(atob(header), (c) => c.charCodeAt(0));
  return JSON.parse(new TextDecoder().decode(bytes));
}

// Asking for JSON is what makes the seller answer with the 402's headers
// rather than with this page again.
async function request(fetchFn, url, headers = {}) {
  return fetchFn(url, { headers: { Accept: "application/json", ...headers } });
}

/** Returns the terms the seller answers an unpaid request with. */
export async function readTerms(fetchFn, url) {
  const response = await request(fetchFn, url);
  const header = response.headers.get("PAYMENT-REQUIRED");
  if (response.status !== 402 || header === null) {
    throw new Error(`the seller answered ${response.status} without payment terms`);
  }
  return decodeHeader(header);
}

// The gno entry of the offer, checked the way the client mechanism checks it
// before anything is signed.
function gnoOffer(terms) {
  const accepted = (terms.accepts ?? []).find(
    (a) => a.scheme === "exact" && typeof a.network === "string" && a.network.startsWith(NETWORK_PREFIX),
  );
  if (accepted === undefined) {
    throw new Error("this resource offers no gno payment");
  }
  if (!AMOUNT.test(accepted.amount) || !DENOMINATION.test(accepted.asset)) {
    throw new Error(`${accepted.amount} ${accepted.asset} is not a gno coin`);
  }
  if (typeof accepted.payTo !== "string" || accepted.payTo === "") {
    throw new Error("the offer names no payee");
  }
  return accepted;
}

// Adena answers ALREADY_CONNECTED and REDUNDANT_CHANGE_REQUEST as failures
// although they mean the wallet is already where the page wants it.
function expectSuccess(response, alsoFine = []) {
  if (response?.status === "success" || alsoFine.includes(response?.type)) {
    return response.data;
  }
  throw new Error(`Adena: ${response?.message || response?.type || "no answer"}`);
}

/**
 * Pays url with Adena: reads the terms, has Adena sign a bank send of the
 * amount to the payee on the offer's chain without broadcasting it, and
 * repeats the request with that payment.
 *
 * Throws before anything is sent for a wallet refusal, a missing network or an
 * offer the page cannot pay, and after sending when no answer came back.
 * Otherwise returns what the paid request answered.
 */
export async function payWithAdena({ adena, fetch: fetchFn, url, report = () => {} }) {
  report("Reading the terms…");
  const terms = await readTerms(fetchFn, url);
  const accepted = gnoOffer(terms);
  const chainId = accepted.network.slice(NETWORK_PREFIX.length);

  report("Connecting to Adena…");
  expectSuccess(await adena.AddEstablish("x402 weather demo"), ["ALREADY_CONNECTED"]);
  const network = expectSuccess(await adena.GetNetwork());
  if (network.chainId !== chainId) {
    report(`Switching Adena to ${chainId}…`);
    const switched = await adena.SwitchNetwork(chainId);
    if (switched?.type === "UNADDED_NETWORK") {
      throw new Error(`Adena has no network ${chainId}; add it in Adena, then pay again`);
    }
    expectSuccess(switched, ["REDUNDANT_CHANGE_REQUEST"]);
  }
  const account = expectSuccess(await adena.GetAccount());

  report("Confirm the payment in Adena…");
  const memo = typeof accepted.extra?.memo === "string" ? accepted.extra.memo : "";
  const signed = expectSuccess(
    await adena.SignTx({
      messages: [
        {
          type: "/bank.MsgSend",
          value: {
            from_address: account.address,
            to_address: accepted.payTo,
            amount: `${accepted.amount}${accepted.asset}`,
          },
        },
      ],
      memo,
    }),
  );

  report("Paying…");
  const payment = {
    x402Version: terms.x402Version,
    resource: terms.resource,
    accepted,
    payload: { transaction: signed.encodedTransaction },
  };
  let response;
  let body;
  try {
    response = await request(fetchFn, url, { "PAYMENT-SIGNATURE": encodeHeader(payment) });
    body = await response.text();
  } catch (err) {
    const cause = err instanceof Error ? err.message : String(err);
    throw new Error(`The payment was sent, but no answer came back (${cause}). Check the account before paying again.`);
  }

  // A settle failure carries its reason in the receipt, a verify refusal in a
  // fresh challenge.
  const receiptHeader = response.headers.get("PAYMENT-RESPONSE");
  const challengeHeader = response.headers.get("PAYMENT-REQUIRED");
  const receipt = receiptHeader === null ? {} : decodeHeader(receiptHeader);
  const challenge = challengeHeader === null ? {} : decodeHeader(challengeHeader);
  return {
    paid: response.ok && receipt.success === true,
    status: response.status,
    body,
    transaction: receipt.transaction ?? "",
    reason: receipt.errorReason || challenge.error || "",
  };
}

/**
 * Says what a payment came to. A payment the chain admitted but has not yet
 * executed comes back unpaid with its transaction, and may still go through.
 */
export function statusLine(result) {
  if (result.paid) {
    return `Paid. Transaction ${result.transaction}.`;
  }
  if (result.transaction !== "") {
    return `Sent, not confirmed yet (${result.reason}): the weather was withheld, but transaction ${result.transaction} may still go through.`;
  }
  return `Not paid (${result.status})${result.reason === "" ? "" : `: ${result.reason}`}`;
}

// How long the page waits for Adena to inject window.adena: it does so from a
// script it loads itself, which can land after the page's load event.
const ADENA_WAIT_MS = 5000;
const ADENA_POLL_MS = 250;

/** Wires the page: this page's URL, copy buttons, the live terms, and Adena. */
export function start(document, window) {
  for (const e of document.querySelectorAll("[data-url]")) {
    e.textContent = window.location.href;
  }

  if (window.navigator.clipboard !== undefined) {
    for (const pre of document.querySelectorAll("pre[data-copy]")) {
      const button = document.createElement("button");
      button.type = "button";
      button.className = "copy";
      button.textContent = "Copy";
      button.addEventListener("click", async () => {
        await window.navigator.clipboard.writeText(pre.querySelector("code").innerText);
        button.textContent = "Copied";
        window.setTimeout(() => (button.textContent = "Copy"), 1500);
      });
      pre.append(button);
    }
  }

  const terms = document.getElementById("terms");
  terms.addEventListener(
    "toggle",
    async () => {
      const out = document.getElementById("terms-json");
      try {
        out.textContent = JSON.stringify(await readTerms(window.fetch.bind(window), window.location.href), null, 2);
      } catch (err) {
        out.textContent = String(err);
      }
    },
    { once: true },
  );

  // Presence alone is not the wallet: an element whose id is "adena" would be
  // window.adena too.
  const offerAdena = () => {
    if (typeof window.adena?.SignTx !== "function") {
      return false;
    }
    document.getElementById("no-adena").hidden = true;
    document.getElementById("pay-adena").hidden = false;
    return true;
  };
  if (!offerAdena()) {
    let waited = 0;
    const poll = window.setInterval(() => {
      waited += ADENA_POLL_MS;
      if (offerAdena() || waited >= ADENA_WAIT_MS) {
        window.clearInterval(poll);
      }
    }, ADENA_POLL_MS);
  }

  const button = document.getElementById("pay");
  const status = document.getElementById("pay-status");
  const result = document.getElementById("pay-result");
  button.addEventListener("click", async () => {
    button.disabled = true;
    result.hidden = true;
    try {
      const paid = await payWithAdena({
        adena: window.adena,
        fetch: window.fetch.bind(window),
        url: window.location.href,
        report: (step) => (status.textContent = step),
      });
      status.textContent = statusLine(paid);
      if (paid.paid) {
        result.querySelector("code").textContent = paid.body;
        result.hidden = false;
      }
    } catch (err) {
      status.textContent = err instanceof Error ? err.message : String(err);
    } finally {
      button.disabled = false;
    }
  });
}
