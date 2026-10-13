import { describe, expect, it, vi } from "vitest";

import { decodeHeader, encodeHeader, payWithAdena, start, statusLine } from "./paywall.js";

const URL = "https://weather.example/weather";
const PAYER = "g1jg8mtutu9khhfwc4nxmuhcpftf0pajdhfvsqf5";
const PAY_TO = "g1nmzz8jc66mgfswfqhuze5ulmyde3ql5hsyffud";
const SIGNED = "c2lnbmVkIHR4"; // what Adena hands back; the page never decodes it
const TX_HASH = "a23105cf9c5ab595e041427d4df85a00b42d8535a7dab19b8a8e07364009ba2a";
const WEATHER = '{"celsius":24,"forecast":"sunny","location":"Paris"}';

function offer(extra = {}) {
  return {
    scheme: "exact",
    network: "gno:onyx-1",
    asset: "ugnot",
    amount: "250000",
    payTo: PAY_TO,
    maxTimeoutSeconds: 300,
    extra: { areFeesSponsored: false, ...extra },
  };
}

function terms(accepted = offer()) {
  return {
    x402Version: 2,
    error: "Payment required",
    resource: { url: URL, description: "Weather data", mimeType: "application/json" },
    accepts: [accepted],
  };
}

function ok(type, data) {
  return { code: 0, status: "success", type, message: "", data };
}

// Adena as the page sees it: every call answers in its WalletResponse envelope.
// The wallet starts on chainId and switches when asked.
function fakeAdena({ chainId = "onyx-1", sign = ok("SIGN_TX", { encodedTransaction: SIGNED }) } = {}) {
  let current = chainId;
  return {
    AddEstablish: vi.fn(async () => ok("CONNECTION_SUCCESS", {})),
    GetNetwork: vi.fn(async () => ok("GET_NETWORK", { chainId: current })),
    SwitchNetwork: vi.fn(async (to) => {
      current = to;
      return ok("SWITCH_NETWORK_SUCCESS", { chainId: to });
    }),
    GetAccount: vi.fn(async () => ok("GET_ACCOUNT", { address: PAYER, chainId: current })),
    SignTx: vi.fn(async () => sign),
  };
}

// The seller as the page sees it: an unpaid request answers 402 with the terms
// in PAYMENT-REQUIRED, a paid one answers paid().
function fakeSeller(offered, paid) {
  return vi.fn(async (_url, init) => {
    const signature = init?.headers?.["PAYMENT-SIGNATURE"];
    if (signature === undefined) {
      return new Response("{}", { status: 402, headers: { "PAYMENT-REQUIRED": encodeHeader(offered) } });
    }
    return paid(decodeHeader(signature));
  });
}

function settled() {
  return settledWith(WEATHER);
}

function settledWith(body) {
  return new Response(body, {
    status: 200,
    headers: {
      "PAYMENT-RESPONSE": encodeHeader({ success: true, transaction: TX_HASH, network: "gno:onyx-1", payer: PAYER }),
    },
  });
}

describe("payWithAdena", () => {
  it("signs one bank send of the amount to the payee, from the Adena account", async () => {
    const adena = fakeAdena();

    await payWithAdena({ adena, fetch: fakeSeller(terms(), settled), url: URL });

    expect(adena.SignTx).toHaveBeenCalledWith({
      messages: [
        {
          type: "/bank.MsgSend",
          value: { from_address: PAYER, to_address: PAY_TO, amount: "250000ugnot" },
        },
      ],
      memo: "",
    });
  });

  // The facilitator relays payload.transaction verbatim, so the page wraps
  // Adena's bytes untouched in the v2 payload a stock client sends.
  it("retries with a v2 payment carrying Adena's signed transaction", async () => {
    let payment;
    const seller = fakeSeller(terms(), (sent) => {
      payment = sent;
      return settled();
    });

    await payWithAdena({ adena: fakeAdena(), fetch: seller, url: URL });

    expect(payment).toEqual({
      x402Version: 2,
      resource: terms().resource,
      accepted: offer(),
      payload: { transaction: SIGNED },
    });
  });

  it("returns the weather and the transaction that paid for it", async () => {
    const result = await payWithAdena({ adena: fakeAdena(), fetch: fakeSeller(terms(), settled), url: URL });

    expect(result).toEqual({
      paid: true,
      status: 200,
      body: WEATHER,
      transaction: TX_HASH,
      reason: "",
    });
  });

  // The facilitator refuses a payment whose memo differs from extra.memo.
  it("signs the memo the offer binds the payment to", async () => {
    const adena = fakeAdena();

    await payWithAdena({ adena, fetch: fakeSeller(terms(offer({ memo: "invoice-42" })), settled), url: URL });

    expect(adena.SignTx.mock.calls[0][0].memo).toBe("invoice-42");
  });

  // Adena signs for the network it is on, and a signature for another chain is
  // refused by the facilitator as signature_invalid.
  it("switches Adena to the offer's chain before signing", async () => {
    const adena = fakeAdena({ chainId: "dev" });

    await payWithAdena({ adena, fetch: fakeSeller(terms(), settled), url: URL });

    expect(adena.SwitchNetwork).toHaveBeenCalledWith("onyx-1");
    expect(adena.SwitchNetwork.mock.invocationCallOrder[0]).toBeLessThan(adena.SignTx.mock.invocationCallOrder[0]);
  });

  it("leaves Adena's network alone when it is already on the offer's chain", async () => {
    const adena = fakeAdena();

    await payWithAdena({ adena, fetch: fakeSeller(terms(), settled), url: URL });

    expect(adena.SwitchNetwork).not.toHaveBeenCalled();
  });

  it("names the chain to add when Adena does not know it", async () => {
    const adena = fakeAdena({ chainId: "dev" });
    adena.SwitchNetwork.mockResolvedValue({
      code: 4000,
      status: "failure",
      type: "UNADDED_NETWORK",
      message: "",
      data: null,
    });

    await expect(payWithAdena({ adena, fetch: fakeSeller(terms(), settled), url: URL })).rejects.toThrow(
      /onyx-1/,
    );
    expect(adena.SignTx).not.toHaveBeenCalled();
  });

  it("sends nothing when the signature is declined", async () => {
    const adena = fakeAdena({
      sign: { code: 4001, status: "failure", type: "TRANSACTION_REJECTED", message: "User rejected", data: null },
    });
    const seller = fakeSeller(terms(), settled);

    await expect(payWithAdena({ adena, fetch: seller, url: URL })).rejects.toThrow(/User rejected/);
    expect(seller).toHaveBeenCalledTimes(1);
  });

  it("reports the reason a refused payment came back with", async () => {
    const refused = () =>
      new Response("{}", {
        status: 402,
        headers: {
          "PAYMENT-REQUIRED": encodeHeader({
            ...terms(),
            error: "invalid_exact_gno_transaction_simulation_failed",
          }),
        },
      });

    const result = await payWithAdena({ adena: fakeAdena(), fetch: fakeSeller(terms(), refused), url: URL });

    expect(result).toMatchObject({
      paid: false,
      status: 402,
      transaction: "",
      reason: "invalid_exact_gno_transaction_simulation_failed",
    });
  });

  it("refuses an offer with no gno payment in it", async () => {
    const adena = fakeAdena();
    const evm = { ...offer(), network: "eip155:8453" };

    await expect(payWithAdena({ adena, fetch: fakeSeller(terms(evm), settled), url: URL })).rejects.toThrow(
      /no gno payment/,
    );
    expect(adena.AddEstablish).not.toHaveBeenCalled();
  });

  // The coin is amount + asset as one string, so an asset that runs into the
  // amount would make Adena sign a different coin than the one shown.
  it("refuses an amount or asset that would sign another coin", async () => {
    for (const accepted of [{ ...offer(), amount: "2.5" }, { ...offer(), asset: "0ugnot" }]) {
      const adena = fakeAdena();

      await expect(
        payWithAdena({ adena, fetch: fakeSeller(terms(accepted), settled), url: URL }),
      ).rejects.toThrow(/not a gno coin/);
      expect(adena.SignTx).not.toHaveBeenCalled();
    }
  });

  // The client mechanism refuses these offers before signing; the page pays the
  // same offers it does.
  it("refuses an offer it would sign a different payment for", async () => {
    for (const extra of [
      { assetTransferMethod: "permit2" },
      { paymentFlow: "settlement" },
      { memo: 42 },
      { memo: "é".repeat(129) },
    ]) {
      const adena = fakeAdena();

      await expect(
        payWithAdena({ adena, fetch: fakeSeller(terms(offer(extra)), settled), url: URL }),
      ).rejects.toThrow();
      expect(adena.SignTx).not.toHaveBeenCalled();
    }
  });

  it("pays an offer that names the method, the flow and a memo of 256 bytes", async () => {
    const adena = fakeAdena();
    const accepted = offer({ assetTransferMethod: "bank-send", paymentFlow: "authorization", memo: "é".repeat(128) });

    const result = await payWithAdena({ adena, fetch: fakeSeller(terms(accepted), settled), url: URL });

    expect(result.paid).toBe(true);
  });

  it("signs nothing when the switch to the offer's chain is declined", async () => {
    const adena = fakeAdena({ chainId: "dev" });
    adena.SwitchNetwork.mockResolvedValue({
      code: 4000,
      status: "failure",
      type: "SWITCH_NETWORK_REJECTED",
      message: "User rejected",
      data: null,
    });

    await expect(payWithAdena({ adena, fetch: fakeSeller(terms(), settled), url: URL })).rejects.toThrow(
      /User rejected/,
    );
    expect(adena.SignTx).not.toHaveBeenCalled();
  });

  it("asks for the terms as JSON, so the seller answers with the header rather than this page", async () => {
    const seller = fakeSeller(terms(), settled);

    await payWithAdena({ adena: fakeAdena(), fetch: seller, url: URL });

    for (const [, init] of seller.mock.calls) {
      expect(init.headers.Accept).toBe("application/json");
    }
  });
});

describe("payWithAdena, again", () => {
  // A second visit finds the site connected and the wallet on the right chain;
  // Adena answers both as failures.
  it("goes on when Adena is already connected and already switched", async () => {
    const adena = fakeAdena({ chainId: "dev" });
    adena.AddEstablish.mockResolvedValue({ code: 4001, status: "failure", type: "ALREADY_CONNECTED", message: "", data: null });
    adena.SwitchNetwork.mockResolvedValue({
      code: 4001,
      status: "failure",
      type: "REDUNDANT_CHANGE_REQUEST",
      message: "",
      data: null,
    });

    const result = await payWithAdena({ adena, fetch: fakeSeller(terms(), settled), url: URL });

    expect(result.paid).toBe(true);
  });

  it("reports the reason a failed settlement came back with", async () => {
    const failed = () =>
      new Response("", {
        status: 402,
        headers: {
          "PAYMENT-RESPONSE": encodeHeader({ success: false, errorReason: "invalid_exact_gno_transaction_failed" }),
        },
      });

    const result = await payWithAdena({ adena: fakeAdena(), fetch: fakeSeller(terms(), failed), url: URL });

    expect(result).toMatchObject({ paid: false, reason: "invalid_exact_gno_transaction_failed" });
  });

  // The seller withholds the weather and passes on the hash, the only way to
  // find out whether the payment still goes through.
  it("keeps the hash of a settlement still pending", async () => {
    const pending = () =>
      new Response("", {
        status: 402,
        headers: {
          "PAYMENT-RESPONSE": encodeHeader({ success: false, errorReason: "settlement_pending", transaction: TX_HASH }),
        },
      });

    const result = await payWithAdena({ adena: fakeAdena(), fetch: fakeSeller(terms(), pending), url: URL });

    expect(result).toMatchObject({ paid: false, transaction: TX_HASH, reason: "settlement_pending" });
  });

  // The node may have admitted the payment before the connection dropped, so
  // the buyer must not be told nothing happened.
  it("says the payment was sent when its answer is lost", async () => {
    const seller = fakeSeller(terms(), () => {
      throw new TypeError("Failed to fetch");
    });

    await expect(payWithAdena({ adena: fakeAdena(), fetch: seller, url: URL })).rejects.toThrow(/was sent/);
  });

  it("asks nothing of any host but the page's own", async () => {
    const seller = fakeSeller(terms(), settled);

    await payWithAdena({ adena: fakeAdena(), fetch: seller, url: URL });

    expect(seller.mock.calls.map(([u]) => u)).toEqual([URL, URL]);
  });
});

describe("statusLine", () => {
  it("names the transaction that paid", () => {
    expect(statusLine({ paid: true, status: 200, transaction: TX_HASH, reason: "" })).toBe(
      `Paid. Transaction ${TX_HASH}.`,
    );
  });

  // The chain admitted the payment and has not executed it yet: the weather is
  // withheld, but the payment may still go through, and the hash is how the
  // buyer finds out.
  it("keeps the hash of a payment still pending", () => {
    const line = statusLine({ paid: false, status: 402, transaction: TX_HASH, reason: "settlement_pending" });

    expect(line).toContain(TX_HASH);
    expect(line).not.toMatch(/^Not paid/);
  });

  it("gives the reason a payment was refused", () => {
    expect(
      statusLine({ paid: false, status: 402, transaction: "", reason: "invalid_exact_gno_transaction_simulation_failed" }),
    ).toBe("Not paid (402): invalid_exact_gno_transaction_simulation_failed");
  });
});

// The parts of the page start() touches, and nothing else. Each element keeps
// the listener start() gives it, so a test can click.
function fakePage() {
  const element = (hidden = false) => {
    const listeners = {};
    const attributes = {};
    const code = { textContent: "", innerText: "" };
    return {
      hidden,
      textContent: "",
      disabled: false,
      dataset: {},
      listeners,
      attributes,
      addEventListener(type, fn) {
        listeners[type] = fn;
      },
      setAttribute(name, value) {
        attributes[name] = value;
      },
      querySelector: () => code,
      append() {},
    };
  };
  const elements = {
    "pay-adena": element(true),
    "no-adena": element(),
    pay: element(),
    "pay-status": element(),
    "pay-result": element(true),
    terms: element(),
    "terms-json": element(),
    sky: element(),
    "paid-temp": element(),
    "paid-forecast": element(),
    "paid-icon": element(),
  };
  const copyable = element();
  const made = [];
  const document = {
    getElementById: (id) => elements[id],
    querySelectorAll: (selector) => (selector === "pre[data-copy]" ? [copyable] : []),
    createElement: () => {
      const e = element();
      made.push(e);
      return e;
    },
  };
  const window = {
    location: { href: URL },
    navigator: {},
    fetch: vi.fn(),
    setTimeout,
    setInterval,
    clearInterval,
    addEventListener() {},
  };
  return { document, window, elements, made };
}

describe("start", () => {
  // Adena injects window.adena from a script it loads itself, which can land
  // after the page has loaded.
  it("offers Adena when it arrives after the page", () => {
    vi.useFakeTimers();
    try {
      const { document, window, elements } = fakePage();
      start(document, window);
      expect(elements["pay-adena"].hidden).toBe(true);

      window.adena = fakeAdena();
      vi.advanceTimersByTime(1000);

      expect(elements["pay-adena"].hidden).toBe(false);
      expect(elements["no-adena"].hidden).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });
});

// paidPage is the page after a click that paid, with the seller serving body.
async function paidPage(body) {
  const page = fakePage();
  page.window.adena = fakeAdena();
  page.window.fetch = fakeSeller(terms(), () => settledWith(body));
  start(page.document, page.window);
  await page.elements.pay.listeners.click();
  return page;
}

describe("start, paying", () => {
  // A button still offered after the weather arrived would sign a second
  // payment for the same reading.
  it("pays on a click, shows the weather and the transaction, and offers no second payment", async () => {
    const { elements } = await paidPage(WEATHER);

    expect(elements["pay-status"].textContent).toBe(`Paid. Transaction ${TX_HASH}.`);
    expect(elements["pay-result"].hidden).toBe(false);
    expect(elements["pay-result"].querySelector("code").textContent).toBe(WEATHER);
    expect(elements.pay.hidden).toBe(true);
  });

  it("writes the reading into the sky: its forecast, its icon and its temperature", async () => {
    for (const [forecast, celsius, shown] of [
      ["sunny", 24, "24"],
      ["cloudy", 17, "17"],
      ["rain", 12.5, "12.5"],
      ["snow", -1, "-1"],
    ]) {
      const { elements } = await paidPage(JSON.stringify({ celsius, forecast, location: "Paris" }));

      expect(elements.sky.dataset.forecast).toBe(forecast);
      expect(elements["paid-icon"].attributes.href).toBe(`#i-${forecast}`);
      expect(elements["paid-temp"].textContent).toBe(shown);
      expect(elements["paid-forecast"].textContent).toBe(forecast);
    }
  });

  // The sky only draws the four forecasts it has colours and an icon for. Any
  // other body is still the weather that was paid for, so it shows as served.
  it("shows a body the sky cannot draw as served, with the transaction", async () => {
    for (const body of [
      "sunny, 24 degrees",
      "null",
      '{"forecast":"sunny"}',
      '{"celsius":"24","forecast":"sunny"}',
      '{"celsius":24}',
      '{"celsius":9,"forecast":"fog","location":"Paris"}',
    ]) {
      const { elements } = await paidPage(body);

      expect(elements["pay-status"].textContent).toBe(`Paid. Transaction ${TX_HASH}.`);
      expect(elements["pay-result"].hidden).toBe(false);
      expect(elements["pay-result"].querySelector("code").textContent).toBe(body);
      expect(elements.sky.dataset.forecast).toBeUndefined();
    }
  });

  it("says why a click paid nothing, and frees the button", async () => {
    const { document, window, elements } = fakePage();
    window.adena = fakeAdena({
      sign: { code: 4001, status: "failure", type: "TRANSACTION_REJECTED", message: "User rejected", data: null },
    });
    window.fetch = fakeSeller(terms(), settled);
    start(document, window);

    await elements.pay.listeners.click();

    expect(elements["pay-status"].textContent).toBe("Adena: User rejected");
    expect(elements["pay-result"].hidden).toBe(true);
    expect(elements.pay.disabled).toBe(false);
    expect(elements.pay.hidden).toBe(false);
  });

  it("says a copy failed rather than leaving the button silent", async () => {
    const { document, window, made } = fakePage();
    window.navigator.clipboard = { writeText: vi.fn(async () => Promise.reject(new Error("denied"))) };
    start(document, window);
    const [copy] = made;

    await copy.listeners.click();

    expect(copy.textContent).toBe("Copy failed");
  });
});

describe("start, without Adena", () => {
  // An element whose id is "adena" is reachable as window.adena too, the DOM's
  // named access, so presence alone is not a wallet.
  it("does not mistake a window.adena without SignTx for the wallet", () => {
    vi.useFakeTimers();
    try {
      const { document, window, elements } = fakePage();
      window.adena = { id: "adena" };
      start(document, window);
      vi.advanceTimersByTime(6000);

      expect(elements["pay-adena"].hidden).toBe(true);
      expect(elements["no-adena"].hidden).toBe(false);
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("encodeHeader and decodeHeader", () => {
  it("round-trip JSON through base64, as x402 headers carry it", () => {
    const value = { error: "payé", accepts: [] };

    expect(decodeHeader(encodeHeader(value))).toEqual(value);
    expect(encodeHeader({ a: 1 })).toBe(btoa('{"a":1}'));
  });
});
