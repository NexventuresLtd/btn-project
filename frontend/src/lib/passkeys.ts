import { startAuthentication, startRegistration, browserSupportsWebAuthn } from "@simplewebauthn/browser";
import { post } from "@/lib/api";
import type { CurrentUser } from "@/auth/AuthProvider";
import { tr } from "@/i18n";

export type PasskeyIssue = "unsupported" | "insecure" | "cancelled" | "none" | "failed";
export class PasskeyError extends Error {
  issue: PasskeyIssue;
  constructor(issue: PasskeyIssue, message: string) {
    super(message);
    this.issue = issue;
  }
}

export function passkeyAvailability(): PasskeyIssue | null {
  if (typeof window === "undefined" || !window.isSecureContext) return "insecure";
  if (!browserSupportsWebAuthn()) return "unsupported";
  return null;
}

function messageFor(issue: PasskeyIssue) {
  return tr(`auth.passkey.${issue}` as "auth.passkey.failed");
}

/** Map a WebAuthn / browser failure to a friendly, translated error. */
export function toPasskeyError(err: unknown): Error {
  if (err instanceof PasskeyError) return err;
  const name = (err as { name?: string })?.name ?? "";
  if (name === "NotAllowedError" || name === "AbortError") return new PasskeyError("cancelled", messageFor("cancelled"));
  if (name === "SecurityError") return new PasskeyError("insecure", messageFor("insecure"));
  if (name === "NotSupportedError") return new PasskeyError("unsupported", messageFor("unsupported"));
  if (name === "InvalidStateError") return err as Error; // e.g. authenticator already registered
  return err instanceof Error ? err : new PasskeyError("failed", messageFor("failed"));
}

/** Discoverable-credential sign-in: begin → browser prompt → finish (creates a normal session). */
export async function signInWithPasskey(): Promise<CurrentUser> {
  const issue = passkeyAvailability();
  if (issue) throw new PasskeyError(issue, messageFor(issue));
  const { challengeId, options } = await post<{ challengeId: string; options: Parameters<typeof startAuthentication>[0]["optionsJSON"] }>("/auth/passkey/begin");
  let credential;
  try {
    credential = await startAuthentication({ optionsJSON: options });
  } catch (e) {
    throw toPasskeyError(e);
  }
  const r = await post<{ user: CurrentUser }>("/auth/passkey/finish", { challengeId, credential });
  return r.user;
}

/** Register a new passkey for the signed-in user. */
export async function registerPasskey(name: string): Promise<void> {
  const issue = passkeyAvailability();
  if (issue) throw new PasskeyError(issue, messageFor(issue));
  const { challengeId, options } = await post<{ challengeId: string; options: Parameters<typeof startRegistration>[0]["optionsJSON"] }>("/account/passkeys/register/begin");
  let credential;
  try {
    credential = await startRegistration({ optionsJSON: options });
  } catch (e) {
    throw toPasskeyError(e);
  }
  await post("/account/passkeys/register/finish", { challengeId, credential, name });
}
