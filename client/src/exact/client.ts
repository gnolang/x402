// The gno mechanism for the x402 "exact" scheme. An x402 client that has never
// heard of gno gains the ability to pay a gno chain by registering this — no fork
// of the client, no patch, no upstream change.
//
// It implements SchemeNetworkClient from @x402/core, which is a scheme name plus
// one method, and it does nothing else: no HTTP, no 402 handling, no
// broadcasting. The client library owns the protocol, the facilitator owns
// settlement, and this owns only the chain-specific step between them.
//
// ExactKeetaScheme in @x402/keeta is the in-tree analogue — same shape, one
// opaque base64 chain-native signed object in the payload.
import { MsgEndpoint, MsgSend, decodeTxMessages } from "@gnolang/gno-js-client";
import { type GasPrice, Tx, type Wallet, uint8ArrayToBase64 } from "@gnolang/tm2-js-client";
import type {
  PaymentPayloadResult,
  PaymentRequirements,
  SchemeNetworkClient,
} from "@x402/core/types";

import { connectedProvider, signForChain } from "../signing.js";

const NAMESPACE = "gno";

// tm2's denomination grammar (std.ValidateDenom), capped at its MaxDenomLength of
// 274 bytes. The coin a payment sends is amount + asset as one string, so an
// asset outside it is either refused by the chain or read as another coin:
// "0ugnot" after "250000" is 2500000ugnot.
const DENOMINATION = /^[a-z/][a-z0-9_.:/-]{2,273}$/;

// How a gno payment moves value: the payer signs a bank send the facilitator
// broadcasts. An offer that omits extra.assetTransferMethod means it.
const ASSET_TRANSFER_METHOD = "bank-send";

// A gno buyer pays its own transaction fee: std.Fee names no payer and the ante
// charges the first signer, so the payer of the payment is also the payer of the
// fee. The requirements carry no gasWanted or gasFee, so the buyer chooses.
//
// The chain takes the whole fee offered, not the gas used, so the fee follows the
// chain's gas price for the gas the payment reserves. That price adapts to load,
// and a payment under it is refused at settle after the resource ran, costing the
// buyer the resource; it is offered GAS_PRICE_MARGIN times over. A block above
// the chain's gas target raises the price's amount by at least 1, so at onyx-1's
// 1ugnot per 1000 gas the margin covers exactly one such block between signing
// and settle. A bank send uses about an eighth of GAS_WANTED.
const GAS_WANTED = 10_000_000n;
const GAS_PRICE_MARGIN = 2n;

// The fee offered when the chain reports no minimum gas price: a node may still
// set one of its own, which the chain does not report, so this is well clear of
// any.
const UNPRICED_GAS_FEE = "1000000ugnot";

/**
 * Returns the gno chain id a CAIP-2-style x402 network string names, or null when
 * the string does not name a gno chain.
 */
export function gnoChainId(network: string): string | null {
  const parts = network.split(":");
  if (parts.length !== 2) {
    return null;
  }
  const [namespace, chainId] = parts;
  if (namespace !== NAMESPACE || chainId === undefined || chainId === "") {
    return null;
  }
  return chainId;
}

/**
 * Reads extra.memo, the memo the requirements bind the payment's transaction to.
 *
 * An absent memo binds nothing. A memo that is present but not a string is a
 * malformed offer rather than an absent one, because signing "" against it would
 * produce a payment the seller refuses for a reason the buyer cannot see.
 */
function requiredMemo(extra: Record<string, unknown> | undefined): string {
  const memo = extra?.["memo"];
  if (memo === undefined) {
    return "";
  }
  if (typeof memo !== "string") {
    throw new Error(`x402-gno: extra.memo is ${typeof memo}, want string`);
  }
  return memo;
}

/**
 * Signs gno payments for the "exact" scheme. Register it against a gno network —
 * or the "gno:*" pattern for every gno chain — on an x402Client.
 */
export class ExactGnoScheme implements SchemeNetworkClient {
  readonly scheme = "exact";

  /**
   * @param wallet a GnoWallet connected to a provider. The connection is
   * required: a gno sequence is sequential, so only the chain knows the next one.
   * Sui and SVM buyers need chain access for their own reasons; only EVM's random
   * nonce lets a buyer sign entirely offline.
   */
  constructor(private readonly wallet: Wallet) {}

  /**
   * Builds the scheme payload: base64 of a fully signed, unbroadcast std.Tx
   * carrying a single bank.MsgSend.
   *
   * The requirements are validated rather than trusted — they arrive from a
   * seller over the network, and a malformed amount or a missing payTo would
   * otherwise become a signed transaction that pays the wrong thing.
   */
  async createPaymentPayload(
    x402Version: number,
    paymentRequirements: PaymentRequirements,
  ): Promise<PaymentPayloadResult> {
    const chainId = gnoChainId(paymentRequirements.network);
    if (chainId === null) {
      throw new Error(`x402-gno: ${paymentRequirements.network} does not name a gno chain`);
    }
    if (paymentRequirements.scheme !== this.scheme) {
      throw new Error(`x402-gno: scheme ${paymentRequirements.scheme} is not ${this.scheme}`);
    }
    if (!DENOMINATION.test(paymentRequirements.asset)) {
      throw new Error(`x402-gno: asset ${paymentRequirements.asset} is not a gno denomination`);
    }
    if (!/^[1-9][0-9]*$/.test(paymentRequirements.amount)) {
      throw new Error(`x402-gno: amount ${paymentRequirements.amount} is not a positive integer`);
    }
    if (typeof paymentRequirements.payTo !== "string" || paymentRequirements.payTo === "") {
      throw new Error("x402-gno: requirements name no payTo");
    }
    // §6.1 reserves both keys: an offer naming a transfer method or a flow this
    // mechanism does not implement is not one it may sign a payment for. A key
    // set to null names nothing, as upstream's SDKs read it.
    const method = paymentRequirements.extra?.["assetTransferMethod"];
    if (method !== undefined && method !== null && method !== ASSET_TRANSFER_METHOD) {
      throw new Error(`x402-gno: assetTransferMethod ${String(method)} is not ${ASSET_TRANSFER_METHOD}`);
    }
    const flow = paymentRequirements.extra?.["paymentFlow"];
    if (flow !== undefined && flow !== null && flow !== "authorization") {
      throw new Error(`x402-gno: paymentFlow ${String(flow)} is not authorization`);
    }

    const memo = requiredMemo(paymentRequirements.extra);
    const from = await this.wallet.getAddress();
    const fee = gasFee(await connectedProvider(this.wallet).getGasPrice());
    const tx: Tx = {
      messages: [
        {
          type_url: MsgEndpoint.MSG_SEND,
          value: MsgSend.encode({
            from_address: from,
            to_address: paymentRequirements.payTo,
            amount: `${paymentRequirements.amount}${paymentRequirements.asset}`,
          }).finish(),
        },
      ],
      fee: { gas_wanted: GAS_WANTED, gas_fee: fee },
      memo,
      signatures: [],
    };

    // Signed against the chain the offer names, not the chain the wallet's node
    // reports. The account number and sequence still come from the wallet's
    // provider, so the wallet has to be pointed at a node on the chain being
    // paid — the offer decides which chain the signature covers, not which node
    // supplies the account state it covers.
    const signed = await signForChain(this.wallet, tx, chainId, decodeTxMessages);

    return {
      x402Version,
      payload: { transaction: uint8ArrayToBase64(Tx.encode(signed).finish()) },
    };
  }
}

/**
 * The fee a payment offers for GAS_WANTED at the chain's gas price, rounded up to
 * a whole unit, or UNPRICED_GAS_FEE when the chain reports none.
 */
function gasFee(price: GasPrice | null): string {
  if (price === null) {
    return UNPRICED_GAS_FEE;
  }
  const gas = BigInt(price.gas);
  const fee = (GAS_WANTED * BigInt(price.amount) * GAS_PRICE_MARGIN + gas - 1n) / gas;
  return `${fee}${price.denom}`;
}
