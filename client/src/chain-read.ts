// How the client mechanism reads the chain before it signs: each read bounded in
// time, and read again when no answer came back. Nothing is signed yet, so a
// repeated read costs nothing; once a payment exists, nothing here applies.

export type ChainReadOptions = {
  /** Reads in total, the first included. */
  attempts: number;
  /** How long one read may go unanswered. */
  timeoutMs: number;
  /** The wait before the next read, times the number of reads made so far. */
  backoffMs: number;
};

// A node that never answers fails the payment in about half a minute.
const DEFAULTS: ChainReadOptions = { attempts: 3, timeoutMs: 10_000, backoffMs: 500 };

class Unanswered extends Error {}

// Whether err means the request got no answer, as opposed to the chain's own
// answer. tm2-js-client's provider sends every RPC through axios, which marks
// its errors and attaches a response only when one came back.
function gotNoAnswer(err: unknown): boolean {
  if (err instanceof Unanswered) {
    return true;
  }
  if (typeof err !== "object" || err === null || !("isAxiosError" in err) || err.isAxiosError !== true) {
    return false;
  }
  if (!("response" in err) || err.response === undefined || err.response === null) {
    return true;
  }
  const { response } = err;
  return typeof response === "object" && "status" in response && typeof response.status === "number" && response.status >= 500;
}

function withTimeout<T>(pending: Promise<T>, ms: number): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeout = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new Unanswered()), ms);
  });
  return Promise.race([pending, timeout]).finally(() => clearTimeout(timer));
}

/**
 * Returns what read resolves to, reading again when a read times out or its
 * request got no answer, up to options.attempts reads. The chain's own answer,
 * such as an account it has never seen, is thrown at once.
 */
export async function readChain<T>(read: () => Promise<T>, options: ChainReadOptions = DEFAULTS): Promise<T> {
  let lastError: unknown;
  for (let made = 1; made <= options.attempts; made++) {
    try {
      return await withTimeout(read(), options.timeoutMs);
    } catch (err) {
      if (!gotNoAnswer(err)) {
        throw err;
      }
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
