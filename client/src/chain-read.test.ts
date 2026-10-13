import { type IncomingMessage, type ServerResponse, createServer } from "node:http";

import { JSONRPCProvider } from "@gnolang/tm2-js-client";
import { afterEach, describe, expect, it, vi } from "vitest";

import { readChain } from "./chain-read.js";

const quick = { attempts: 3, timeoutMs: 50, backoffMs: 1 };

describe("readChain", () => {
  it("asks again when a read fails", async () => {
    const read = vi
      .fn<() => Promise<string>>()
      .mockRejectedValueOnce(new Error("socket hang up"))
      .mockRejectedValueOnce(new Error("socket hang up"))
      .mockResolvedValue("7");

    await expect(readChain(read, quick)).resolves.toBe("7");
    expect(read).toHaveBeenCalledTimes(3);
  });

  it("throws the last failure after its attempts", async () => {
    const read = vi.fn<() => Promise<string>>().mockRejectedValue(new Error("account is not initialized"));

    await expect(readChain(read, quick)).rejects.toThrow("account is not initialized");
    expect(read).toHaveBeenCalledTimes(3);
  });

  it("gives up on a read that never answers, saying how often it asked", async () => {
    const read = vi.fn(() => new Promise<string>(() => {}));

    await expect(readChain(read, quick)).rejects.toThrow(/did not answer.*3 attempts/);
    expect(read).toHaveBeenCalledTimes(3);
  });
});

function rpcRequest(body: string): { id: unknown; method: unknown } {
  const parsed: unknown = JSON.parse(body);
  if (typeof parsed !== "object" || parsed === null || !("id" in parsed) || !("method" in parsed)) {
    throw new Error(`not a JSON-RPC request: ${body}`);
  }
  return { id: parsed.id, method: parsed.method };
}

// The provider tm2-js-client ships, against a node that answers status and
// abci_query and fails the first abci_query in the way under test. What the
// provider throws when no answer came back is decided by the HTTP stack beneath
// it, so only a real provider shows the read is asked again.
describe("readChain with tm2-js-client's provider", () => {
  let close: (() => void) | undefined;
  afterEach(() => close?.());

  async function node(failFirstQuery: (res: ServerResponse) => void): Promise<{ url: string; queries: () => number }> {
    let queries = 0;
    const server = createServer((req: IncomingMessage, res: ServerResponse) => {
      let body = "";
      req.on("data", (chunk: Buffer) => (body += chunk.toString()));
      req.on("end", () => {
        const { id, method } = rpcRequest(body);
        if (method === "abci_query" && ++queries === 1) {
          failFirstQuery(res);
          return;
        }
        const result =
          method === "status"
            ? { node_info: { version: "v1" } }
            : { response: { ResponseBase: { Error: null, Data: null, Events: null, Log: "", Info: "" }, Height: "0" } };
        res.setHeader("Content-Type", "application/json");
        res.end(JSON.stringify({ jsonrpc: "2.0", id, result }));
      });
    });
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    close = () => server.close();
    const address = server.address();
    if (address === null || typeof address === "string") {
      throw new Error("the node listens on no TCP port");
    }
    return { url: `http://127.0.0.1:${address.port}`, queries: () => queries };
  }

  it("asks again when the node drops the connection", async () => {
    const { url, queries } = await node((res) => res.socket?.destroy());
    const provider = await JSONRPCProvider.create(url);

    await expect(readChain(() => provider.getGasPrice(), quick)).resolves.toBeNull();
    expect(queries()).toBe(2);
  });

  it("asks again when a proxy in front of the node fails", async () => {
    const { url, queries } = await node((res) => {
      res.statusCode = 502;
      res.end("bad gateway");
    });
    const provider = await JSONRPCProvider.create(url);

    await expect(readChain(() => provider.getGasPrice(), quick)).resolves.toBeNull();
    expect(queries()).toBe(2);
  });
});
