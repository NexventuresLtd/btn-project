/**
 * Offline runtime: one per tab. Opens the signed-in user's encrypted store, keeps the sync engine running,
 * holds the offline-unlock session and the global prompts (sign-out guard, revoked device, update ready).
 * React reads it through `useOfflineState()`; non-React code (AuthProvider, api) calls the functions below.
 */
import { Dexie } from "dexie";
import type { CurrentUser } from "@/auth/AuthProvider";
import { fromB64 } from "./core/crypto";
import { UserDB, getDeviceDb, userDbName, type DeviceUser } from "./core/db";
import { initDevice } from "./core/device";
import { SyncEngine, type EngineState, type SyncOutcome } from "./core/engine";
import { connectivity } from "./core/connectivity";
import { readQuota, requestPersistence, type QuotaInfo } from "./core/quota";
import { OfflineStore } from "./core/store";
import { httpTransport, TransportError, type OfflinePackageResponse, type SyncTransport } from "./core/transport";
import { PinLockedError, Vault } from "./core/vault";
import { WrongSecretError } from "./core/crypto";
import { deviceHeaders } from "./core/device";
import { acrossTabs, singleFlight, withRetry } from "./core/retry";
import { refreshSession } from "@/lib/api";

export const DEFAULT_UNLOCK_DAYS = 14;

export interface StoredPackage {
  userId: string;
  deviceId: string;
  issuedAt: string;
  expiresAt: string;
  unlockDays: number;
  signature: string | null;
  /** Ed25519 signature checked on receipt (null = unsigned/local or not checkable in this browser) */
  verified: boolean | null;
  user: CurrentUser;
}

export type Phase = "none" | "opening" | "locked" | "unlocked";

export interface Counts {
  pending: number;
  conflict: number;
  rejected: number;
  issues: number;
}

export interface OfflineState {
  phase: Phase;
  userId: string | null;
  /** user restored from the offline package while the server is unreachable */
  offlineUser: CurrentUser | null;
  counts: Counts;
  engine: EngineState | null;
  slots: string[];
  packageExpiresAt: string | null;
  quota: QuotaInfo | null;
  revoked: boolean;
  updateReady: boolean;
  dirtyForms: number;
  signOutPrompt: { pending: number } | null;
  /** an online user without an offline PIN yet */
  suggestPin: boolean;
  /** the data key has no durable slot yet (no server device key, no password): a PIN must be set now */
  pinRequired: boolean;
  error: string | null;
}

const ZERO: Counts = { pending: 0, conflict: 0, rejected: 0, issues: 0 };
let state: OfflineState = {
  phase: "none", userId: null, offlineUser: null, counts: ZERO, engine: null, slots: [], packageExpiresAt: null, quota: null,
  revoked: false, updateReady: false, dirtyForms: 0, signOutPrompt: null, suggestPin: false, pinRequired: false, error: null,
};
const listeners = new Set<() => void>();
const set = (p: Partial<OfflineState>) => {
  state = { ...state, ...p };
  listeners.forEach((l) => l());
};
export const getOfflineState = () => state;
export const subscribeOffline = (l: () => void): (() => void) => {
  listeners.add(l);
  return () => void listeners.delete(l);
};

let store: OfflineStore | null = null;
let engine: SyncEngine | null = null;
let transport: SyncTransport = httpTransport({ headers: deviceHeaders, refresh: () => refreshSession() });
let deviceSecret: CryptoKey | null = null;
let deviceId = "";
let loginPassword: { value: string; until: number } | null = null;
let unsubs: (() => void)[] = [];
let periodic: ReturnType<typeof setInterval> | null = null;
const syncedHandlers = new Set<(o: SyncOutcome) => void>();

export const getStore = () => store;
export const getEngine = () => engine;
/** test hook */
export const __setTransport = (t: SyncTransport) => {
  transport = t;
};

/** LoginPage hands the typed password over so a "password" unlock slot can be created after OTP (memory only, 10 min). */
export function rememberLoginPassword(password: string) {
  freshSignIn = true;
  loginPassword = { value: password, until: Date.now() + 10 * 60_000 };
}
const takeLoginPassword = () => {
  const p = loginPassword && loginPassword.until > Date.now() ? loginPassword.value : null;
  loginPassword = null;
  return p;
};

export function onSynced(fn: (o: SyncOutcome) => void) {
  syncedHandlers.add(fn);
  return () => void syncedHandlers.delete(fn);
}

async function device() {
  if (!deviceSecret) {
    const d = await initDevice();
    deviceSecret = d.secret;
    deviceId = d.deviceId;
  }
  return { deviceId, secret: deviceSecret };
}

async function refreshCounts() {
  if (!store) return;
  const counts = await store.counts();
  set({ counts });
}

async function refreshQuota() {
  set({ quota: await readQuota() });
}

/** Wire store + engine after the vault is unlocked. */
async function attach(s: OfflineStore) {
  detach();
  store = s;
  engine = new SyncEngine({
    store: s,
    transport,
    onRevoked: () => void handleRevoked(),
    exclusive: webLock(`btn-sync-${s.userId}`),
    onAuthRequired: () => set({ error: "auth" }),
    onSynced: (o) => {
      void refreshCounts();
      syncedHandlers.forEach((h) => h(o));
    },
  });
  await engine.init();
  unsubs.push(s.subscribe(() => void refreshCounts()));
  unsubs.push(engine.subscribe(() => set({ engine: engine?.state ?? null })));
  unsubs.push(connectivity.subscribe(() => {
    if (connectivity.get().status === "online") void engine?.kick();
  }));
  periodic = setInterval(() => {
    if (connectivity.get().status === "online") void engine?.sync();
    void s.prune();
  }, 2 * 60_000);
  set({ phase: "unlocked", engine: engine.state, slots: await s.vault.slotKinds() });
  await refreshCounts();
  void refreshQuota();
  void requestPersistence();
  void s.prune();
  if (connectivity.get().status !== "offline") void engine.sync();
}

/** Web Locks wrapper (all modern browsers); runs unlocked where unsupported — the server is idempotent anyway. */
function webLock(name: string) {
  const locks = (typeof navigator !== "undefined" ? (navigator as Navigator & { locks?: { request<T>(n: string, cb: () => Promise<T>): Promise<T> } }).locks : undefined);
  return locks ? <T,>(fn: () => Promise<T>) => locks.request(name, fn) : undefined;
}

function detach() {
  unsubs.forEach((u) => u());
  unsubs = [];
  if (periodic) clearInterval(periodic);
  periodic = null;
  engine?.stop();
  engine = null;
  store?.close();
  store = null;
}

async function savePackage(s: OfflineStore, user: CurrentUser, pkg: OfflinePackageResponse | null) {
  const now = new Date();
  const unlockDays = pkg?.unlockDays ?? pkg?.package.unlockDays ?? DEFAULT_UNLOCK_DAYS;
  const cap = new Date(now.getTime() + unlockDays * 86_400_000).toISOString();
  const stored: StoredPackage = {
    userId: user.id,
    deviceId,
    issuedAt: pkg?.package.issuedAt ?? now.toISOString(),
    expiresAt: pkg?.package.expiresAt && pkg.package.expiresAt < cap ? pkg.package.expiresAt : cap,
    unlockDays,
    signature: pkg?.signature ?? null,
    verified: pkg?.verified ?? null,
    user,
  };
  await s.setSecretMeta("package", stored);
  await s.setMeta("lastOnlineAt", now.toISOString());
  await getDeviceDb().users.put({ userId: user.id, name: user.name, email: maskEmail(user.email), lastOnlineAt: now.toISOString(), expiresAt: stored.expiresAt });
  set({ packageExpiresAt: stored.expiresAt });
}

function maskEmail(e: string) {
  const [name = "", domain = ""] = e.split("@");
  return `${name.slice(0, 2)}${"•".repeat(Math.max(1, name.length - 2))}@${domain}`;
}

/* ───────── offline package: fetched only after a real sign-in or near expiry (never per page load) ───────── */

const RENEW_BEFORE_MS = 3 * 86_400_000; // renew when < 3 days of the offline window are left
let freshSignIn = false;
/** Called by the sign-in pages: the next openForUser may fetch a new offline package. */
export function markFreshSignIn() {
  freshSignIn = true;
}

interface StoredKek {
  key: CryptoKey; // non-extractable session-slot KEK derived from the server device key
  expiresAt: string;
}
const kekKey = (userId: string) => `sessionKek:${userId}`;
async function storedKek(userId: string): Promise<StoredKek | null> {
  const v = (await getDeviceDb().kv.get(kekKey(userId)))?.value as StoredKek | undefined;
  return v && Date.parse(v.expiresAt) > Date.now() ? v : null;
}
const pkgFetchedKey = (userId: string) => `btn.offlinePkg.fetchedAt.${userId}`;

/**
 * POST /auth/offline-package — one call in flight per tab, serialised across tabs (a tab that finds a package
 * fetched by another tab in the last minute reuses the key that tab stored), bounded back-off with jitter on
 * 429 honouring Retry-After. Never throws (except revoked); null = no package this time.
 */
const fetchPackageOnce = singleFlight(() =>
  acrossTabs("btn-offline-package", async (): Promise<OfflinePackageResponse | null> => {
    const uid = state.userId ?? "";
    try {
      const at = Number(localStorage.getItem(pkgFetchedKey(uid)));
      if (at && Date.now() - at < 60_000) return null; // another tab just did it; its key is in IndexedDB
    } catch { /* storage blocked */ }
    try {
      const pkg = await withRetry(() => transport.offlinePackage(deviceId), { attempts: 4, shared: true, baseMs: 3_000 });
      try {
        localStorage.setItem(pkgFetchedKey(uid), String(Date.now()));
      } catch { /* storage blocked */ }
      if (pkg.verified === false) return null; // forged / corrupted package: ignore it
      return pkg;
    } catch (e) {
      if (e instanceof TransportError && e.kind === "revoked") {
        void handleRevoked();
        throw e;
      }
      return null; // still rate limited, offline, or no package support: next sign-in / renewal tries again
    }
  }),
);

async function keepSessionKek(vault: Vault, userId: string, pkg: OfflinePackageResponse | null): Promise<CryptoKey | null> {
  if (!pkg?.deviceKey) return null;
  const kek = await vault.sessionKek(fromB64(pkg.deviceKey));
  const expiresAt = pkg.package.expiresAt ?? new Date(Date.now() + DEFAULT_UNLOCK_DAYS * 86_400_000).toISOString();
  await getDeviceDb().kv.put({ key: kekKey(userId), value: { key: kek, expiresAt } satisfies StoredKek });
  return kek;
}

/**
 * A user is signed in online (AuthProvider got /auth/me). Open (or create) their store. The offline package is
 * fetched only after a real sign-in, for a new store, or when the offline window is about to end; otherwise the
 * store opens with the session-slot key kept on this device (used only while the server confirms the session).
 */
export async function openForUser(user: CurrentUser): Promise<void> {
  if (state.phase === "unlocked" && state.userId === user.id && store) {
    set({ offlineUser: null });
    engine?.resume();
    void renewIfNeeded(user);
    return;
  }
  if (state.phase === "opening" && state.userId === user.id) return;
  set({ phase: "opening", userId: user.id, offlineUser: null, error: null, revoked: false });
  try {
    const { secret } = await device();
    const db = new UserDB(userDbName(user.id));
    const vault = new Vault(db, user.id, secret);
    const initialised = await vault.isInitialised();
    const kept = await storedKek(user.id);
    const nearExpiry = !kept || Date.parse(kept.expiresAt) - Date.now() < RENEW_BEFORE_MS;
    const fresh = freshSignIn;
    freshSignIn = false;
    const pkg = fresh || !initialised || nearExpiry ? await fetchPackageOnce() : null;
    // a new key from this fetch, else the one kept on the device (possibly stored by another tab just now)
    const kek = (await keepSessionKek(vault, user.id, pkg)) ?? (await storedKek(user.id))?.key ?? null;
    const password = takeLoginPassword();
    if (!initialised) {
      await vault.create();
      if (kek) await vault.addSessionSlotKek(kek);
      if (password) await vault.addPasswordSlot(password);
      // nothing to wrap the key with yet: it lives in memory only until the user sets a PIN (prompt is not dismissible)
      if (!kek && !password) set({ pinRequired: true });
    } else {
      let ok = false;
      if (kek) ok = await vault.unlockWithSessionKek(kek).then(() => true, () => false);
      if (!ok && password) ok = await vault.unlockWithPassword(password).then(() => true, () => false);
      if (!ok) {
        db.close();
        set({ phase: "locked", slots: await vault.slotKinds() });
        return;
      }
      // keep slots fresh: server key rotated, password changed
      if (pkg?.deviceKey && kek) await vault.addSessionSlotKek(kek);
      if (password) await vault.addPasswordSlot(password);
    }
    const s = new OfflineStore(vault, deviceId);
    const existing = initialised ? await s.getSecretMeta<StoredPackage>("package").catch(() => undefined) : undefined;
    if (pkg || !existing) await savePackage(s, user, pkg);
    else set({ packageExpiresAt: existing.expiresAt });
    await attach(s);
    const kinds = await vault.slotKinds();
    set({ suggestPin: !kinds.includes("pin"), slots: kinds });
  } catch (e) {
    set({ phase: "none", error: e instanceof Error ? e.message : "open failed" });
  }
}

/** Renew the offline package only when the window is about to end (or right after a real sign-in). */
async function renewIfNeeded(user: CurrentUser) {
  if (!store) return;
  const current = await store.getSecretMeta<StoredPackage>("package").catch(() => undefined);
  const fresh = freshSignIn;
  freshSignIn = false;
  if (!fresh && current && Date.parse(current.expiresAt) - Date.now() > RENEW_BEFORE_MS) return;
  const pkg = await fetchPackageOnce().catch(() => null);
  if (!pkg) return;
  const kek = await keepSessionKek(store.vault, user.id, pkg);
  if (kek) await store.vault.addSessionSlotKek(kek);
  await savePackage(store, user, pkg);
}

export type UnlockError = "wrong" | "pinLocked" | "expired" | "clock" | "noData";

/** Unlock with the offline PIN or password. `online` = server reachable and session valid (no expiry check). */
export async function unlock(userId: string, secret: string, kind: "pin" | "password", opts: { online?: boolean } = {}): Promise<{ ok: true } | { ok: false; error: UnlockError; failures?: number }> {
  const { secret: dev } = await device();
  const known = await getDeviceDb().users.get(userId);
  const exists = known || (await Dexie.exists(userDbName(userId)));
  if (!exists) return { ok: false, error: "noData" };
  const db = new UserDB(userDbName(userId));
  const vault = new Vault(db, userId, dev);
  try {
    if (kind === "pin") await vault.unlockWithPin(secret);
    else await vault.unlockWithPassword(secret);
  } catch (e) {
    const failures = await vault.pinFailures().catch(() => 0);
    db.close();
    if (e instanceof PinLockedError) return { ok: false, error: "pinLocked" };
    if (e instanceof WrongSecretError) return { ok: false, error: "wrong", failures };
    throw e;
  }
  const s = new OfflineStore(vault, deviceId);
  const pkg = await s.getSecretMeta<StoredPackage>("package");
  const now = Date.now();
  const lastSeen = (await s.getMeta<number>("lastSeenClock")) ?? 0;
  if (!opts.online) {
    if (lastSeen && now < lastSeen - 10 * 60_000) {
      s.close();
      return { ok: false, error: "clock" };
    }
    if (!pkg || Date.parse(pkg.expiresAt) < now) {
      s.close();
      return { ok: false, error: "expired" };
    }
  }
  await s.setMeta("lastSeenClock", Math.max(now, lastSeen));
  set({ userId, offlineUser: opts.online ? null : pkg?.user ?? null, packageExpiresAt: pkg?.expiresAt ?? null });
  await attach(s);
  set({ suggestPin: !(await vault.slotKinds()).includes("pin") });
  return { ok: true };
}

export async function setOfflinePin(pin: string) {
  if (!store) throw new Error("locked");
  await store.vault.addPinSlot(pin);
  set({ slots: await store.vault.slotKinds(), suggestPin: false, pinRequired: false });
}

export async function removeOfflinePin() {
  if (!store) return;
  const kinds = await store.vault.slotKinds();
  if (kinds.length <= 1) throw new Error("last slot");
  await store.vault.removeSlot("pin");
  set({ slots: await store.vault.slotKinds(), suggestPin: true });
}

export const dismissPinSuggestion = () => set({ suggestPin: false });

/** Users who keep offline data on this device (for the unlock screen). */
export async function deviceUsers(): Promise<DeviceUser[]> {
  try {
    return await getDeviceDb().users.orderBy("lastOnlineAt").reverse().toArray();
  } catch {
    return [];
  }
}

export function lockNow() {
  detach();
  set({ phase: state.userId ? "locked" : "none", offlineUser: null, counts: ZERO, engine: null });
}

/** Erase one user's local database and keys from this device (OFF-13). */
export async function wipeUser(userId: string) {
  if (state.userId === userId) detach();
  await Dexie.delete(userDbName(userId));
  await getDeviceDb().users.delete(userId);
  await getDeviceDb().kv.delete(`sessionKek:${userId}`);
  if (state.userId === userId) set({ phase: "none", userId: null, offlineUser: null, counts: ZERO, engine: null, slots: [], packageExpiresAt: null, suggestPin: false, signOutPrompt: null, revoked: false });
}

/* ───────── sign-out guard (OFF-14) ───────── */

let signOutResolve: ((ok: boolean) => void) | null = null;

/**
 * Called by AuthProvider.logout before the server call. Resolves true when sign-out may go ahead:
 * immediately when nothing is unsynced, otherwise after the user confirmed the data-loss warning.
 */
export async function confirmSignOut(): Promise<boolean> {
  if (!store) return true;
  await refreshCounts();
  const pending = state.counts.pending + state.counts.issues;
  if (pending === 0) return true;
  return new Promise<boolean>((resolve) => {
    signOutResolve = resolve;
    set({ signOutPrompt: { pending } });
  });
}

export function answerSignOut(ok: boolean) {
  set({ signOutPrompt: null });
  signOutResolve?.(ok);
  signOutResolve = null;
}

/** After the server session ended: the device keeps nothing of this user. */
export async function afterSignOut() {
  const id = state.userId;
  // another tab holding the database open can block deletion: never hang the sign-out on it
  if (id) await Promise.race([wipeUser(id), new Promise((r) => setTimeout(r, 5_000))]);
}

/* ───────── revoked device (OFF-12) ───────── */

/** Device revoked by an administrator (sync 403 or lib/api DEVICE_REVOKED_EVENT). */
export async function handleRevoked() {
  set({ revoked: true });
  if (store) {
    await refreshCounts();
    if (state.counts.pending === 0 && state.userId) await wipeUser(state.userId).then(() => set({ revoked: true }));
  }
}

/* ───────── sync controls ───────── */

export async function syncNow(): Promise<SyncOutcome | null> {
  if (!engine) return null;
  await connectivity.probe();
  return engine.kick();
}

/** Background Sync API: let the service worker wake us when connectivity returns, even if the tab was idle. */
export async function requestBackgroundSync() {
  try {
    const reg = await navigator.serviceWorker?.ready;
    const sync = (reg as ServiceWorkerRegistration & { sync?: { register(tag: string): Promise<void> } })?.sync;
    await sync?.register("btn-outbox");
  } catch { /* not supported — the in-page engine still syncs on reconnect */ }
}

/* ───────── update-when-safe (OFF-21) ───────── */

let applyUpdate: (() => void) | null = null;

export function registerDirtyForm(): () => void {
  set({ dirtyForms: state.dirtyForms + 1 });
  let done = false;
  return () => {
    if (done) return;
    done = true;
    set({ dirtyForms: Math.max(0, state.dirtyForms - 1) });
    if (state.dirtyForms === 0 && state.updateReady && document.visibilityState === "hidden") applyUpdate?.();
  };
}

export function updateAvailable(apply: () => void) {
  applyUpdate = apply;
  if (state.dirtyForms === 0 && document.visibilityState === "hidden") return apply();
  set({ updateReady: true });
  // apply by itself the next time the app is in the background with no unsaved form (never mid-edit)
  const onHide = () => {
    if (document.visibilityState !== "hidden" || state.dirtyForms > 0) return;
    document.removeEventListener("visibilitychange", onHide);
    apply();
  };
  document.addEventListener("visibilitychange", onHide);
}

export const applyPendingUpdate = () => applyUpdate?.();

export { refreshQuota, refreshCounts };

/** PUT/DELETE /api/auth/offline-pin: the server only learns that a PIN exists (it keeps the session alive for the offline window). */
export async function markPinOnServer(set: boolean) {
  try {
    await fetch("/api/auth/offline-pin", { method: set ? "PUT" : "DELETE", credentials: "include", headers: { "X-Requested-With": "btn-ops", Accept: "application/json", ...deviceHeaders() } });
  } catch { /* offline: retried next time the PIN is changed; harmless */ }
}
