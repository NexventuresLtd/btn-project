/**
 * The vault owns the data key of one user's offline database (OFF-12, OFF-13, NFR-S3).
 * Slots: "pin" (offline PIN), "password" (account password as of the last online sign-in) and "session"
 * (server-held per-device key, only obtainable with a live signed-in session). The PIN verifier is the
 * PIN slot itself — a wrong PIN fails AES-GCM authentication; nothing about the PIN is ever sent.
 */
import { PBKDF2_ITERATIONS, WrongSecretError, decryptJson, deriveKek, encryptJson, generateDataKey, kekFromMachineSecret, randomBytes, unwrapDataKey, wrapDataKey, type Box } from "./crypto";
import type { KeySlot, UserDB } from "./db";

export const MAX_PIN_FAILURES = 5;

export class PinLockedError extends Error {
  constructor() {
    super("pin locked");
    this.name = "PinLockedError";
  }
}

/** ≥ 6 digits, not one repeated digit, not a straight run. */
export function validatePin(pin: string): "ok" | "length" | "digits" | "weak" {
  if (!/^\d+$/.test(pin)) return "digits";
  if (pin.length < 6 || pin.length > 12) return "length";
  if (/^(\d)\1+$/.test(pin)) return "weak";
  const up = "0123456789012", down = "9876543210987";
  if (up.includes(pin) || down.includes(pin)) return "weak";
  return "ok";
}

export class Vault {
  private dek: CryptoKey | null = null;
  readonly db: UserDB;
  readonly userId: string;
  private readonly deviceSecret: CryptoKey | null;
  readonly iterations: number;

  constructor(db: UserDB, userId: string, deviceSecret: CryptoKey | null, iterations = PBKDF2_ITERATIONS) {
    this.db = db;
    this.userId = userId;
    this.deviceSecret = deviceSecret;
    this.iterations = iterations;
  }

  get unlocked() {
    return this.dek !== null;
  }

  async slotKinds(): Promise<KeySlot["kind"][]> {
    return (await this.db.slots.toArray()).map((s) => s.kind);
  }

  async isInitialised() {
    return (await this.db.slots.count()) > 0;
  }

  /** First use on this device: a fresh data key, held in memory until slots are added. */
  async create() {
    this.dek = await generateDataKey();
  }

  lock() {
    this.dek = null;
  }

  private key(): CryptoKey {
    if (!this.dek) throw new Error("vault locked");
    return this.dek;
  }

  private async addHumanSlot(kind: "pin" | "password", secret: string) {
    const salt = randomBytes(16);
    const kek = await deriveKek(secret, salt, this.iterations, this.deviceSecret);
    const w = await wrapDataKey(this.key(), kek);
    await this.db.slots.put({ kind, salt, iterations: this.iterations, ...w, createdAt: new Date().toISOString(), failures: 0 });
  }

  addPinSlot(pin: string) {
    if (validatePin(pin) !== "ok") throw new Error("invalid pin");
    return this.addHumanSlot("pin", pin);
  }

  addPasswordSlot(password: string) {
    return this.addHumanSlot("password", password);
  }

  async addSessionSlot(machineSecret: Uint8Array) {
    const kek = await kekFromMachineSecret(machineSecret, this.deviceSecret);
    const w = await wrapDataKey(this.key(), kek);
    await this.db.slots.put({ kind: "session", salt: null, iterations: 0, ...w, createdAt: new Date().toISOString() });
  }

  removeSlot(kind: KeySlot["kind"]) {
    return this.db.slots.delete(kind);
  }

  private async unlockHuman(kind: "pin" | "password", secret: string) {
    const slot = await this.db.slots.get(kind);
    if (!slot || !slot.salt) throw kind === "pin" ? new PinLockedError() : new WrongSecretError();
    const kek = await deriveKek(secret, slot.salt, slot.iterations, this.deviceSecret);
    try {
      this.dek = await unwrapDataKey(slot, kek);
      if (slot.failures) await this.db.slots.update(kind, { failures: 0 });
    } catch (e) {
      if (kind === "pin") {
        const failures = (slot.failures ?? 0) + 1;
        // after too many wrong PINs the PIN slot is destroyed: password or online sign-in still work, data is kept
        if (failures >= MAX_PIN_FAILURES) {
          await this.db.slots.delete("pin");
          throw new PinLockedError();
        }
        await this.db.slots.update("pin", { failures });
      }
      throw e;
    }
  }

  unlockWithPin(pin: string) {
    return this.unlockHuman("pin", pin);
  }

  unlockWithPassword(password: string) {
    return this.unlockHuman("password", password);
  }

  async pinFailures(): Promise<number> {
    return (await this.db.slots.get("pin"))?.failures ?? 0;
  }

  /** The session-slot KEK for a server device key (non-extractable; may be kept in IndexedDB until the package expires). */
  sessionKek(machineSecret: Uint8Array): Promise<CryptoKey> {
    return kekFromMachineSecret(machineSecret, this.deviceSecret);
  }

  async addSessionSlotKek(kek: CryptoKey) {
    const w = await wrapDataKey(this.key(), kek);
    await this.db.slots.put({ kind: "session", salt: null, iterations: 0, ...w, createdAt: new Date().toISOString() });
  }

  async unlockWithSessionKek(kek: CryptoKey) {
    const slot = await this.db.slots.get("session");
    if (!slot) throw new WrongSecretError();
    this.dek = await unwrapDataKey(slot, kek);
  }

  async unlockWithSession(machineSecret: Uint8Array) {
    const slot = await this.db.slots.get("session");
    if (!slot) throw new WrongSecretError();
    const kek = await kekFromMachineSecret(machineSecret, this.deviceSecret);
    this.dek = await unwrapDataKey(slot, kek);
  }

  async encrypt(value: unknown, aad: string): Promise<Box> {
    return encryptJson(this.key(), value, `${this.userId}|${aad}`);
  }

  async decrypt<T>(box: Box, aad: string): Promise<T> {
    return decryptJson<T>(this.key(), box, `${this.userId}|${aad}`);
  }
}
