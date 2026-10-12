import type { SessionAccountInfo } from "@gnolang/gno-js-client";
import type { ABCIAccount, GasPrice, Provider, Status, Tx } from "@gnolang/tm2-js-client";

// A provider for tests. It answers the questions a payment asks — the chain id,
// the account, the gas price — and refuses the rest, so a payment path that
// reaches the network some other way fails the test loudly.
export class FakeProvider implements Provider {
  constructor(
    private readonly reportedChain: string,
    private readonly gasPrice: GasPrice | null = null,
  ) {}

  async getStatus(): Promise<Status> {
    return {
      node_info: {
        version_set: [],
        net_address: "",
        network: this.reportedChain,
        software: "",
        version: "",
        channels: "",
        monkier: "",
        other: { tx_index: "", rpc_address: "" },
      },
      sync_info: {
        latest_block_hash: "",
        latest_app_hash: "",
        latest_block_height: "1",
        latest_block_time: "",
        catching_up: false,
      },
      validator_info: {
        address: "",
        pub_key: { type: "", value: "" },
        voting_power: "0",
      },
    };
  }

  async getAccount(address: string): Promise<ABCIAccount> {
    return {
      BaseAccount: {
        address,
        coins: "1000000000ugnot",
        public_key: null,
        account_number: "12",
        sequence: "7",
      },
    };
  }

  async getGasPrice(): Promise<GasPrice | null> {
    return this.gasPrice;
  }

  private unreachable(): never {
    throw new Error("a payment must not reach this provider method");
  }

  getBalance(): Promise<number> {
    return this.unreachable();
  }
  getAccountSequence(): Promise<number> {
    return this.unreachable();
  }
  getAccountNumber(): Promise<number> {
    return this.unreachable();
  }
  getBlock(): never {
    return this.unreachable();
  }
  getBlockResult(): never {
    return this.unreachable();
  }
  getBlockNumber(): Promise<number> {
    return this.unreachable();
  }
  getNetwork(): never {
    return this.unreachable();
  }
  getConsensusParams(): never {
    return this.unreachable();
  }
  abciQuery(): never {
    return this.unreachable();
  }
  estimateGas(): Promise<bigint> {
    return this.unreachable();
  }
  getTransaction(): never {
    return this.unreachable();
  }
  sendTransaction(): never {
    return this.unreachable();
  }
  waitForTransaction(): Promise<Tx> {
    return this.unreachable();
  }
}

// The session account's own number and sequence, distinct from the master's
// above so a signature over the wrong pair shows.
export const SESSION_ACCOUNT = { account_number: "34", sequence: "3" } as const;

// A gno provider for tests: every address is a session of master, with
// SESSION_ACCOUNT's number and sequence, and no other account holds a session.
export class FakeGnoProvider extends FakeProvider {
  constructor(
    reportedChain: string,
    private readonly master: string,
    gasPrice: GasPrice | null = null,
  ) {
    super(reportedChain, gasPrice);
  }

  async getSession(masterAddress: string, sessionAddress: string): Promise<SessionAccountInfo> {
    if (masterAddress !== this.master) {
      throw new Error(`no session ${sessionAddress} for master account ${masterAddress}`);
    }
    return {
      address: sessionAddress,
      master_address: masterAddress,
      allow_paths: ["bank/send"],
      ...SESSION_ACCOUNT,
    };
  }
}
