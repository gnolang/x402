import type { GnoProvider } from "@gnolang/gno-js-client";
import {
  type Any,
  type Provider,
  PubKeySecp256k1,
  Secp256k1PubKeyType,
  type Tx,
  type TxSignPayload,
  type Wallet,
  encodeCharacterSet,
  parseSignCoin,
  sortedJsonStringify,
  stringToUTF8,
} from "@gnolang/tm2-js-client";

/** Expands encoded message values so the sign doc can sort them. */
export type DecodeTxMessages = (messages: Any[]) => unknown[];

/**
 * Signs a transaction against the chain it names, rather than the chain the
 * wallet happens to be connected to.
 *
 * Wallet.signTransaction takes the chain id from the node — it reads /status and
 * offers no override — so the chain a payment committed to was whichever one the
 * provider happened to report. An x402 offer names its chain, and that is the one
 * the signature has to cover.
 *
 * Only the chain id is taken from the offer. The account number and sequence
 * below come from the connected provider, because a gno sequence is sequential
 * and only the chain holds the next one — so this decides which chain a signature
 * commits to, not which node supplies the account state it commits to. Signing
 * for one chain against another's account state produces a payment that chain's
 * ante cannot reproduce, and the facilitator reports it signature_invalid.
 *
 * Avoiding /status has a second effect worth knowing: /status is the only
 * response that decodes a validator's bech32 address, which is the call that
 * forces a pinned @scure/base. Nothing on the account path decodes one.
 *
 * The sign doc is byte-identical to Wallet.signTransaction's for the same
 * inputs, and a test holds it that way. The account fields are the strings the
 * chain returned: they are quoted in the sign doc, so passing numbers would
 * change the bytes and invalidate every signature.
 *
 * With master set, the wallet holds a session key registered under that
 * account: the sign doc covers the session account's own number and sequence,
 * and the signature names the session, so the chain verifies it against the
 * session's key and limits.
 */
export async function signForChain(
  wallet: Wallet,
  tx: Tx,
  chainId: string,
  decodeTxMessages: DecodeTxMessages,
  master?: string,
): Promise<Tx> {
  const { fee } = tx;
  if (fee === undefined) {
    throw new Error("x402-gno: transaction names no fee, so nothing can be signed");
  }

  const provider = connectedProvider(wallet);
  const address = await wallet.getAddress();
  const account =
    master === undefined
      ? (await provider.getAccount(address)).BaseAccount
      : await sessionAccount(provider, master, address);

  const payload: TxSignPayload = {
    chain_id: chainId,
    account_number: account.account_number,
    sequence: account.sequence,
    fee: {
      amount: parseSignCoin(fee.gas_fee),
      gas: fee.gas_wanted.toString(10),
    },
    msgs: decodeTxMessages(tx.messages),
    memo: tx.memo,
  };
  const signBytes = stringToUTF8(encodeCharacterSet(sortedJsonStringify(payload)));

  const signer = wallet.getSigner();
  const publicKey = await signer.getPublicKey();

  return {
    ...tx,
    signatures: [
      ...tx.signatures,
      {
        pub_key: {
          type_url: Secp256k1PubKeyType,
          value: PubKeySecp256k1.encode({ key: publicKey }).finish(),
        },
        signature: await signer.signData(signBytes),
        session_addr: master === undefined ? "" : address,
      },
    ],
  };
}

type SessionReader = Pick<GnoProvider, "getSession">;

function readsSessions(provider: Provider): provider is Provider & SessionReader {
  return "getSession" in provider && typeof provider.getSession === "function";
}

/**
 * Returns the account number and sequence of the session account that session
 * holds under master, as the chain renders them.
 */
async function sessionAccount(
  provider: Provider,
  master: string,
  session: string,
): Promise<{ account_number: string; sequence: string }> {
  if (!readsSessions(provider)) {
    throw new Error(
      "x402-gno: paying as a session needs a gno provider, such as GnoJSONRPCProvider, to read the session account",
    );
  }
  const { account_number, sequence } = await provider.getSession(master, session);
  if (account_number === undefined || sequence === undefined) {
    throw new Error(
      `x402-gno: the chain reports no account number or sequence for session ${session} of ${master}`,
    );
  }
  return { account_number, sequence };
}

/**
 * Returns the wallet's provider, which a payment reads its account and the gas
 * price from.
 *
 * getProvider is the one wallet accessor that does not check the connection
 * itself, so an unconnected wallet would fail on the first property access with a
 * bare TypeError — and an x402 client wraps that into a failure to create a
 * payment payload, which names neither the wallet nor the connection.
 */
export function connectedProvider(wallet: Wallet): Provider {
  const provider = wallet.getProvider();
  if (provider === undefined) {
    throw new Error("x402-gno: the wallet has no provider connected, so the chain cannot be read");
  }
  return provider;
}
