// How the client mechanism reads the chain before it signs: each read bounded in
// time, and made again when it fails. Nothing is signed yet, so a repeated read
// costs nothing; once a payment exists, nothing here applies.
//
// Every failure is read again, the chain's own answers included. What the
// provider throws when no answer came back is decided by the HTTP stack beneath
// it, which differs between Node and a browser and is not part of its API, while
// a refusal asked three times is still thrown, a second and a half later.

export type ChainReadOptions = {
  /** Reads in total, the first included. */
  attempts: number;
  /** How long one read may go unanswered. */
  timeoutMs: number;
  /** The wait before the next read, times the number of reads made so far. */
  backoffMs: number;
};

// A node that never answers fails the payment in about half a minute.
const DEFAULTS = { attempts: 3, timeoutMs: 10_000, backoffMs: 500 } as const satisfies ChainReadOptions;

class Unanswered extends Error {}

// Stops waiting for pending after ms. It does not cancel the request: the
// provider takes no abort signal, so an abandoned request runs on until the node
// or the operating system closes it.
function withTimeout<T>(pending: Promise<T>, ms: number): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeout = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new Unanswered()), ms);
  });
  return Promise.race([pending, timeout]).finally(() => clearTimeout(timer));
}

/**
 * Returns what read resolves to, reading again when a read fails or times out,
 * up to options.attempts reads, and throws the last failure.
 */
export async function readChain<T>(read: () => Promise<T>, options: ChainReadOptions = DEFAULTS): Promise<T> {
  let lastError: unknown;
  for (let made = 1; made <= options.attempts; made++) {
    try {
      return await withTimeout(read(), options.timeoutMs);
    } catch (err) {
      lastError = err;
      if (made < options.attempts) {
        await new Promise((resolve) => setTimeout(resolve, options.backoffMs * made));
      }
    }
  }
  if (lastError instanceof Unanswered) {
    throw new Error(
      `x402-gno: the chain did not answer within ${options.timeoutMs} ms, after ${options.attempts} attempts`,
    );
  }
  throw lastError;
}
