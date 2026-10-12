import { MsgEndpoint, MsgSend, decodeTxMessages } from "@gnolang/gno-js-client";
import { Tx, Wallet, uint8ArrayToBase64 } from "@gnolang/tm2-js-client";
import { describe, expect, it } from "vitest";

import { FakeProvider } from "./fake-provider.js";
import { signForChain } from "./signing.js";

// The well-known test1 account. Nothing is broadcast, so the key only has to be
// the same one on both sides of the comparison.
const MNEMONIC =
  "source bonus chronic canvas draft south burst lottery vacant surface solve popular case indicate oppose farm nothing bullet exhibit title speed wink action roast";

const REPORTED_CHAIN = "dev";
const PAY_TO = "g1u7y667z64x2h7vc6fmpcprgey4ck233jaww9zq";

async function connectedWallet(): Promise<Wallet> {
  const wallet = await Wallet.fromMnemonic(MNEMONIC);
  wallet.connect(new FakeProvider(REPORTED_CHAIN));
  return wallet;
}

function paymentTx(from: string): Tx {
  return {
    messages: [
      {
        type_url: MsgEndpoint.MSG_SEND,
        value: MsgSend.encode({
          from_address: from,
          to_address: PAY_TO,
          amount: "250000ugnot",
        }).finish(),
      },
    ],
    fee: { gas_wanted: 10_000_000n, gas_fee: "1000000ugnot" },
    memo: "x402-interop",
    signatures: [],
  };
}

function encoded(tx: Tx): string {
  return uint8ArrayToBase64(Tx.encode(tx).finish());
}

describe("signForChain", () => {
  // The safety net. This reimplements the wallet's sign doc so the chain id can
  // come from the offer instead of the node, and a sign doc that differs by one
  // byte makes every payment invalid while looking like a facilitator bug.
  it("produces the same bytes as the wallet's own signing", async () => {
    const wallet = await connectedWallet();
    const from = await wallet.getAddress();

    const ours = await signForChain(wallet, paymentTx(from), REPORTED_CHAIN, decodeTxMessages);
    const theirs = await wallet.signTransaction(paymentTx(from), decodeTxMessages);

    expect(encoded(ours)).toBe(encoded(theirs));
  });

  // What the whole change is for: the chain a signature commits to follows the
  // offer rather than whichever chain the provider reports.
  //
  // It says nothing about the rest of the sign doc. This provider answers with
  // one account number and sequence for every chain, so no test here can show
  // that those two fields still come from the connected node — which is why
  // paying a second chain also means connecting to it.
  it("signs against the chain it is given rather than the one the node reports", async () => {
    const wallet = await connectedWallet();
    const from = await wallet.getAddress();

    const elsewhere = await signForChain(wallet, paymentTx(from), "test14", decodeTxMessages);
    const reported = await signForChain(wallet, paymentTx(from), REPORTED_CHAIN, decodeTxMessages);

    expect(encoded(elsewhere)).not.toBe(encoded(reported));
  });

  it("refuses a transaction that names no fee", async () => {
    const wallet = await connectedWallet();
    const from = await wallet.getAddress();
    const { fee: _dropped, ...feeless } = paymentTx(from);

    await expect(signForChain(wallet, feeless, REPORTED_CHAIN, decodeTxMessages)).rejects.toThrow(
      /names no fee/,
    );
  });
});
