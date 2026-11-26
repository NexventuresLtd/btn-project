/**
 * /auth/me without hammering the auth rate limit: one request in flight per tab, serialised across tabs,
 * and a tab reuses an answer another tab got a moment ago (BroadcastChannel, memory only — nothing stored).
 * Rate-limited / overloaded answers (429, 50x) are retried with back-off and NEVER mean "signed out".
 */
import { acrossTabs, isTransient, retryDelay, singleFlight, withRetry, type RetryOptions } from "@/offline/core/retry";

const SHARE_MS = 3_000;
let lastShared: { at: number; user: unknown } | null = null;
const channel = typeof BroadcastChannel !== "undefined" ? new BroadcastChannel("btn-auth-me") : null;
if (channel) channel.onmessage = (e: MessageEvent<{ at: number; user: unknown }>) => (lastShared = e.data);

/** Wrap the /auth/me fetcher (which returns null for 401). */
export function sharedMe<T>(fetcher: () => Promise<T>, retry: RetryOptions = {}): () => Promise<T> {
  return singleFlight(() =>
    acrossTabs("btn-auth-me", async () => {
      // reuse only an answer ANOTHER tab got moments ago (this tab's own answers are never replayed)
      if (lastShared && Date.now() - lastShared.at < SHARE_MS) {
        const u = lastShared.user as T;
        lastShared = null;
        return u;
      }
      const user = await withRetry(fetcher, { attempts: 3, shared: true, ...retry });
      channel?.postMessage({ at: Date.now(), user });
      return user;
    }),
  );
}

/** Sign-out: never reuse a shared answer from before it. */
export function forgetSharedMe() {
  lastShared = null;
}

/** TanStack retry policy for /auth/me: keep retrying transient errors (bounded), never other errors. */
export const meRetry = (count: number, err: unknown) => isTransient(err) && count < 6;
export const meRetryDelay = (count: number, err: unknown) => retryDelay(count + 1, err, { baseMs: 3_000, maxMs: 60_000 });

/**
 * What the app shows for the current session state. A transient failure (429/50x) with no user yet is
 * "still loading", never "signed out" — RequireAuth keeps the loader instead of redirecting to sign-in.
 */
export function authState<U>(data: U | null | undefined, error: unknown, offlineUser: U | null) {
  const offline = data === undefined && (error as { status?: number; code?: string } | null)?.status === 0;
  const transient = data === undefined && isTransient(error);
  return { user: data ?? (offline ? offlineUser : null), offline, transient };
}
