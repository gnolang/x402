import { describe, expect, it, vi } from "vitest";

import { readChain } from "./chain-read.js";

// What axios throws when a request got no answer: a reset connection, a refused
// one, a DNS failure. tm2-js-client's provider sends every RPC through axios.
function noAnswer(): Error {
  return Object.assign(new Error("socket hang up"), { isAxiosError: true, code: "ECONNRESET" });
}

const quick = { attempts: 3, timeoutMs: 50, backoffMs: 1 };

describe("readChain", () => {
  // A public testnet RPC over a lossy path drops the odd request, and nothing
  // has been signed yet, so asking again costs nothing.
  it("asks again when a read gets no answer", async () => {
    const read = vi.fn().mockRejectedValueOnce(noAnswer()).mockRejectedValueOnce(noAnswer()).mockResolvedValue("7");

    await expect(readChain(read, quick)).resolves.toBe("7");
    expect(read).toHaveBeenCalledTimes(3);
  });

  // The chain's own answer is the same however often it is asked.
  it("does not ask again when the chain answered", async () => {
    const read = vi.fn().mockRejectedValue(new Error("account is not initialized"));

    await expect(readChain(read, quick)).rejects.toThrow("account is not initialized");
    expect(read).toHaveBeenCalledTimes(1);
  });

  it("asks again when a proxy in front of the node fails", async () => {
    const badGateway = Object.assign(new Error("Request failed with status code 502"), {
      isAxiosError: true,
      response: { status: 502 },
    });
    const read = vi.fn().mockRejectedValueOnce(badGateway).mockResolvedValue("7");

    await expect(readChain(read, quick)).resolves.toBe("7");
  });

  it("gives up on a read that never answers, saying how often it asked", async () => {
    const read = vi.fn(() => new Promise<string>(() => {}));

    await expect(readChain(read, quick)).rejects.toThrow(/did not answer.*3 attempts/);
    expect(read).toHaveBeenCalledTimes(3);
  });

  it("gives up after its attempts on reads that keep getting no answer", async () => {
    const read = vi.fn().mockRejectedValue(noAnswer());

    await expect(readChain(read, quick)).rejects.toThrow(/socket hang up/);
    expect(read).toHaveBeenCalledTimes(3);
  });
});
