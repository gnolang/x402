import { MsgEndpoint, MsgSend, type SessionAccountInfo, decodeTxMessages } from "@gnolang/gno-js-client";
import { Tx, Wallet, uint8ArrayToBase64 } from "@gnolang/tm2-js-client";
import { describe, expect, it } from "vitest";

import { FakeGnoProvider, FakeProvider, SESSION_ACCOUNT } from "./fake-provider.js";
import { signForChain } from "./signing.js";

// The well-known test1 account. Nothing is broadcast, so the key only has to be
// the same one on both sides of the comparison.
const MNEMONIC =
  "source bonus chronic canvas draft south burst lottery vacant surface solve popular case indicate oppose farm nothing bullet exhibit title speed wink action roast";

// A session key, public like test1's and never funded: e2e/testdata registers
// it as a session of a master account.
const SESSION_MNEMONIC =
  "dance carry grunt uphold cloth wire usage usage clock reduce surprise page destroy reopen multiply away defense promote person web cigar immense armed enough";

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

describe("signForChain as a session", () => {
  // The chain verifies a session signature against the session account's own
  // number and sequence, and reads the session's key at session_addr under the
  // master that MsgSend.from names. Signing over the master's pair instead
  // produces a payment the facilitator reports signature_invalid.
  it("signs the wallet's sign doc over the session account's number and sequence", async () => {
    const master = await (await Wallet.fromMnemonic(MNEMONIC)).getAddress();
    const session = await Wallet.fromMnemonic(SESSION_MNEMONIC);
    session.connect(new FakeGnoProvider(REPORTED_CHAIN, master));

    const ours = await signForChain(session, paymentTx(master), REPORTED_CHAIN, decodeTxMessages, master);
    const theirs = await session.signTransaction(paymentTx(master), decodeTxMessages, {
      accountNumber: SESSION_ACCOUNT.account_number,
      sequence: SESSION_ACCOUNT.sequence,
    });

    expect(ours.signatures).toEqual([
      { ...theirs.signatures[0], session_addr: await session.getAddress() },
    ]);
  });

  // A tm2 provider reads accounts only, so the session's number and sequence
  // cannot be known and nothing would be signed over them.
  it("refuses a provider that cannot read sessions", async () => {
    const master = await (await Wallet.fromMnemonic(MNEMONIC)).getAddress();
    const session = await Wallet.fromMnemonic(SESSION_MNEMONIC);
    session.connect(new FakeProvider(REPORTED_CHAIN));

    await expect(
      signForChain(session, paymentTx(master), REPORTED_CHAIN, decodeTxMessages, master),
    ).rejects.toThrow(/needs a gno provider/);
  });

  // The sign doc quotes the sequence the chain renders, so a session rendered
  // without one leaves nothing to sign over.
  it("refuses a session the chain renders without a sequence", async () => {
    class NoSequence extends FakeGnoProvider {
      override async getSession(masterAddress: string, sessionAddress: string): Promise<SessionAccountInfo> {
        const { sequence: _dropped, ...rendered } = await super.getSession(masterAddress, sessionAddress);
        return rendered;
      }
    }
    const master = await (await Wallet.fromMnemonic(MNEMONIC)).getAddress();
    const session = await Wallet.fromMnemonic(SESSION_MNEMONIC);
    session.connect(new NoSequence(REPORTED_CHAIN, master));

    await expect(
      signForChain(session, paymentTx(master), REPORTED_CHAIN, decodeTxMessages, master),
    ).rejects.toThrow(/no account number or sequence for session/);
  });
});
