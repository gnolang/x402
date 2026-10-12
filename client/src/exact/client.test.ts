import { MsgEndpoint, MsgSend } from "@gnolang/gno-js-client";
import { type ABCIAccount, type GasPrice, Tx, Wallet, base64ToUint8Array } from "@gnolang/tm2-js-client";
import type { PaymentRequirements } from "@x402/core/types";
import { describe, expect, it } from "vitest";

import { FakeGnoProvider, FakeProvider } from "../fake-provider.js";
import { ExactGnoScheme, gnoChainId } from "./client.js";

// The well-known test1 account. Nothing here is broadcast, so the key only has
// to be a valid one.
const MNEMONIC =
  "source bonus chronic canvas draft south burst lottery vacant surface solve popular case indicate oppose farm nothing bullet exhibit title speed wink action roast";

// test1's address standing in for a master, and a session key, public like
// test1's and never funded.
const MASTER = "g1jg8mtutu9khhfwc4nxmuhcpftf0pajdhfvsqf5";
const SESSION_MNEMONIC =
  "dance carry grunt uphold cloth wire usage usage clock reduce surprise page destroy reopen multiply away defense promote person web cigar immense armed enough";

const PAY_TO = "g1u7y667z64x2h7vc6fmpcprgey4ck233jaww9zq";

function offer(overrides: Partial<PaymentRequirements> = {}): PaymentRequirements {
  return {
    scheme: "exact",
    network: "gno:dev",
    amount: "250000",
    asset: "ugnot",
    payTo: PAY_TO,
    maxTimeoutSeconds: 60,
    extra: {},
    ...overrides,
  };
}

// The wallet is deliberately left unconnected. Every case below must be refused
// by the mechanism's own validation, so none of them may reach a provider — and
// if one did, the error would name the missing connection instead of the offer,
// which is what asserting the message rules out.
async function scheme(): Promise<ExactGnoScheme> {
  return new ExactGnoScheme(await Wallet.fromMnemonic(MNEMONIC));
}

describe("gnoChainId", () => {
  // The CAIP-2 two-part rule. This is the only implementation of it in the
  // repository — the Go half concatenates "gno:" onto a configured chain id and
  // string-compares the result, so nothing there splits a network string.
  const chains: Record<string, string | null> = {
    "gno:dev": "dev",
    "gno:test14": "test14",
    "gno:portal-loop": "portal-loop",
    // A chain id holding a colon splits into three, and CAIP-2 names exactly two
    // parts. Accepting it would make one network string mean two different offers.
    "gno:test:14": null,
    "gno:": null,
    gno: null,
    // Another chain's namespace, and one that merely starts the same way.
    "eip155:8453": null,
    "gnoland:dev": null,
    "": null,
  };

  for (const [network, want] of Object.entries(chains)) {
    it(`reads ${JSON.stringify(network)} as ${JSON.stringify(want)}`, () => {
      expect(gnoChainId(network)).toBe(want);
    });
  }
});

describe("ExactGnoScheme.createPaymentPayload", () => {
  // Each refusal has its own message because the buyer is the only party that
  // can act on it: the seller published the offer and the facilitator never sees
  // the payment, so a signed transaction against a malformed offer would be
  // refused later for a reason naming the payload rather than the offer.
  const refusals: Record<string, { requirements: PaymentRequirements; message: RegExp }> = {
    "a network that names no gno chain": {
      requirements: offer({ network: "eip155:8453" }),
      message: /does not name a gno chain/,
    },
    "a chain id that is not one CAIP-2 part": {
      requirements: offer({ network: "gno:test:14" }),
      message: /does not name a gno chain/,
    },
    "another scheme": {
      requirements: offer({ scheme: "upto" }),
      message: /scheme upto is not exact/,
    },
    // The coin sent is amount + asset as one string, so an asset outside the
    // chain's denomination grammar is either unparseable there or another coin.
    "an upper-case asset": {
      requirements: offer({ asset: "UGNOT" }),
      message: /asset UGNOT is not a gno denomination/,
    },
    "an asset that runs into the amount": {
      requirements: offer({ asset: "0ugnot" }),
      message: /asset 0ugnot is not a gno denomination/,
    },
    "an asset naming a second coin": {
      requirements: offer({ asset: "ugnot,1000000ugnot" }),
      message: /asset ugnot,1000000ugnot is not a gno denomination/,
    },
    "an asset too short to be a denomination": {
      requirements: offer({ asset: "ug" }),
      message: /asset ug is not a gno denomination/,
    },
    "an asset longer than the chain stores": {
      requirements: offer({ asset: `/${"a".repeat(274)}` }),
      message: /is not a gno denomination/,
    },
    "a dollar-denominated amount": {
      requirements: offer({ amount: "$0.01" }),
      message: /is not a positive integer/,
    },
    "a zero amount": {
      requirements: offer({ amount: "0" }),
      message: /is not a positive integer/,
    },
    "a negative amount": {
      requirements: offer({ amount: "-250000" }),
      message: /is not a positive integer/,
    },
    "a fractional amount": {
      requirements: offer({ amount: "0.25" }),
      message: /is not a positive integer/,
    },
    "an empty amount": {
      requirements: offer({ amount: "" }),
      message: /is not a positive integer/,
    },
    "an empty payTo": {
      requirements: offer({ payTo: "" }),
      message: /no payTo/,
    },
    "another transfer method": {
      requirements: offer({ extra: { assetTransferMethod: "eip3009" } }),
      message: /assetTransferMethod eip3009 is not bank-send/,
    },
    "a flow other than authorization": {
      requirements: offer({ extra: { paymentFlow: "upfront" } }),
      message: /paymentFlow upfront is not authorization/,
    },
  };

  for (const [name, { requirements, message }] of Object.entries(refusals)) {
    it(`refuses ${name}`, async () => {
      const mechanism = await scheme();
      await expect(mechanism.createPaymentPayload(2, requirements)).rejects.toThrow(message);
    });
  }

  // A memo that is present but not a string is a malformed offer, not an absent
  // one: signing "" against it produces a payment the seller refuses for a memo
  // mismatch, which the buyer cannot diagnose from its own side.
  it("refuses a non-string extra.memo", async () => {
    const mechanism = await scheme();
    await expect(
      mechanism.createPaymentPayload(2, offer({ extra: { memo: 42 } })),
    ).rejects.toThrow(/extra\.memo is number, want string/);
  });

  // An absent memo binds nothing and must not be confused with a malformed one,
  // so it has to get past validation. Reaching signing is the proof: signing is
  // the first step that needs the provider this wallet does not have, so that is
  // the failure a well-formed offer produces here.
  it("accepts an absent extra.memo and reaches signing", async () => {
    const mechanism = await scheme();
    await expect(mechanism.createPaymentPayload(2, offer())).rejects.toThrow(
      /no provider connected/,
    );
  });

  // Naming the defaults is the same offer as omitting them, so it has to get
  // past validation too.
  it("accepts bank-send and authorization named explicitly and reaches signing", async () => {
    const mechanism = await scheme();
    await expect(
      mechanism.createPaymentPayload(
        2,
        offer({ extra: { assetTransferMethod: "bank-send", paymentFlow: "authorization" } }),
      ),
    ).rejects.toThrow(/no provider connected/);
  });

  // A null transfer method or flow names nothing, as upstream's SDKs read it, so
  // it is the same offer as omitting them.
  it("accepts a null transfer method and flow and reaches signing", async () => {
    const mechanism = await scheme();
    await expect(
      mechanism.createPaymentPayload(
        2,
        offer({ extra: { assetTransferMethod: null, paymentFlow: null } }),
      ),
    ).rejects.toThrow(/no provider connected/);
  });

  for (const asset of ["atom", "/gno.land/r/demo/foo20:foo"]) {
    it(`accepts the ${asset} denomination and reaches signing`, async () => {
      const mechanism = await scheme();
      await expect(mechanism.createPaymentPayload(2, offer({ asset }))).rejects.toThrow(
        /no provider connected/,
      );
    });
  }
});

// The signed transaction a payload carries, decoded.
function transactionOf(payload: Record<string, unknown>): Tx {
  const transaction = payload["transaction"];
  if (typeof transaction !== "string") {
    throw new Error(`payload transaction is ${typeof transaction}, want string`);
  }
  return Tx.decode(base64ToUint8Array(transaction));
}

// The payment a connected buyer signs for the default offer, against a chain
// whose gas price is gasPrice, decoded back from the payload.
async function signedPayment(gasPrice: GasPrice | null): Promise<{ keys: string[]; tx: Tx }> {
  const wallet = await Wallet.fromMnemonic(MNEMONIC);
  wallet.connect(new FakeProvider("dev", gasPrice));
  const { payload } = await new ExactGnoScheme(wallet).createPaymentPayload(2, offer());
  return { keys: Object.keys(payload), tx: transactionOf(payload) };
}

const ONYX_GAS_PRICE: GasPrice = { amount: 1, denom: "ugnot", gas: 1000 };

describe("ExactGnoScheme payment", () => {
  // The facilitator relays the payload verbatim and reads one field of it, so a
  // second key would be a field nobody reads and every party still carries.
  it("carries nothing but the signed transaction", async () => {
    const { keys, tx } = await signedPayment(ONYX_GAS_PRICE);

    expect(keys).toEqual(["transaction"]);
    expect(tx.messages.map((m) => m.type_url)).toEqual([MsgEndpoint.MSG_SEND]);
    expect(tx.signatures).toHaveLength(1);
  });

  // The chain takes the whole fee offered, not the gas used, so the fee follows
  // the chain's own price for the gas the payment reserves. The price adapts to
  // load, and a fee under it is refused at settle after the resource ran, so it
  // is offered twice over.
  it("offers twice the chain's gas price for the gas it reserves", async () => {
    const { tx } = await signedPayment(ONYX_GAS_PRICE);

    expect(tx.fee?.gas_wanted).toBe(10_000_000n);
    expect(tx.fee?.gas_fee).toBe("20000ugnot");
  });

  // A fee is a whole number of the denomination's smallest unit, and one rounded
  // down would offer less than the price.
  it("rounds a fractional fee up", async () => {
    const { tx } = await signedPayment({ amount: 3, denom: "ugnot", gas: 7 });

    expect(tx.fee?.gas_fee).toBe("8571429ugnot");
  });

  // A request dropped before anything is signed loses nothing, so the payment
  // survives the node failing to answer its first reads.
  it("signs through reads that got no answer the first time", async () => {
    const dropped = () => Object.assign(new Error("socket hang up"), { isAxiosError: true });
    class Lossy extends FakeProvider {
      private answered = new Set<string>();
      private once<T>(name: string, read: () => Promise<T>): Promise<T> {
        if (!this.answered.has(name)) {
          this.answered.add(name);
          return Promise.reject(dropped());
        }
        return read();
      }
      override getGasPrice(): Promise<GasPrice | null> {
        return this.once("gas", () => super.getGasPrice());
      }
      override getAccount(address: string): Promise<ABCIAccount> {
        return this.once("account", () => super.getAccount(address));
      }
    }
    const wallet = await Wallet.fromMnemonic(MNEMONIC);
    wallet.connect(new Lossy("dev", ONYX_GAS_PRICE));

    const { payload } = await new ExactGnoScheme(wallet).createPaymentPayload(2, offer());

    expect(transactionOf(payload).signatures).toHaveLength(1);
  });

  // A chain with no minimum gas price may still have nodes that set one of their
  // own, which the chain does not report, so the fee is one well clear of any.
  it("offers a fee well clear of any minimum when the chain names no gas price", async () => {
    const { tx } = await signedPayment(null);

    expect(tx.fee?.gas_fee).toBe("1000000ugnot");
  });

  // A buyer passing an unset environment variable straight through gives an
  // undefined master, which has to mean the wallet's own account.
  it("pays from the wallet's own account when master is undefined", async () => {
    const wallet = await Wallet.fromMnemonic(MNEMONIC);
    wallet.connect(new FakeProvider("dev", ONYX_GAS_PRICE));

    const { payload } = await new ExactGnoScheme(wallet, { master: undefined }).createPaymentPayload(
      2,
      offer(),
    );
    const tx = transactionOf(payload);

    expect(tx.messages.map((m) => MsgSend.decode(m.value).from_address)).toEqual([
      await wallet.getAddress(),
    ]);
    expect(tx.signatures.map((s) => s.session_addr)).toEqual([""]);
  });
});

describe("ExactGnoScheme as a session", () => {
  // The session key signs and the master's coins move: the chain charges the
  // account MsgSend.from names, within the limits it holds for the session the
  // signature names.
  it("pays from the master's account under the session's signature", async () => {
    const wallet = await Wallet.fromMnemonic(SESSION_MNEMONIC);
    wallet.connect(new FakeGnoProvider("dev", MASTER, ONYX_GAS_PRICE));

    const { payload } = await new ExactGnoScheme(wallet, { master: MASTER }).createPaymentPayload(
      2,
      offer(),
    );
    const tx = transactionOf(payload);

    expect(tx.messages.map((m) => MsgSend.decode(m.value).from_address)).toEqual([MASTER]);
    expect(tx.signatures.map((s) => s.session_addr)).toEqual([await wallet.getAddress()]);
  });
});
