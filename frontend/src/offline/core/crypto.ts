/**
 * Web Crypto helpers for the offline store (SRS OFF-13, NFR-S3).
 *
 * Key hierarchy
 *   device secret  — random non-extractable HMAC-SHA-256 key generated once per browser profile and kept in
 *                    IndexedDB (a CryptoKey can be stored but never read back as bytes).
 *   KEK            — key-encryption key for one "slot": PBKDF2-SHA-256(secret, salt, ≥310 000 iterations), then
 *                    HMAC(device secret, ·) so a copied database cannot be opened on another device.
 *   data key (DEK) — random AES-GCM-256 key that encrypts every record; stored only wrapped by one or more KEKs
 *                    (PIN slot, password slot, online-session slot) and held in memory while unlocked.
 * Every ciphertext carries additional authenticated data (user id + table + row id) so rows cannot be swapped.
 */

export const PBKDF2_ITERATIONS = 310_000;
const subtle = () => globalThis.crypto.subtle;
const te = new TextEncoder();
const td = new TextDecoder();

export class WrongSecretError extends Error {
  constructor() {
    super("wrong secret");
    this.name = "WrongSecretError";
  }
}

export const randomBytes = (n: number) => globalThis.crypto.getRandomValues(new Uint8Array(n));

export function toB64(bytes: ArrayBuffer | Uint8Array): string {
  const u = bytes instanceof Uint8Array ? bytes : new Uint8Array(bytes);
  let s = "";
  for (let i = 0; i < u.length; i += 0x8000) s += String.fromCharCode(...u.subarray(i, i + 0x8000));
  return btoa(s);
}

export function fromB64(s: string): Uint8Array<ArrayBuffer> {
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

const buf = (u: Uint8Array): Uint8Array<ArrayBuffer> => new Uint8Array(u);

/** A fresh non-extractable device secret. */
export async function createDeviceSecret(): Promise<CryptoKey> {
  return subtle().generateKey({ name: "HMAC", hash: "SHA-256", length: 256 }, false, ["sign"]);
}

async function kekFromBits(bits: ArrayBuffer, deviceSecret: CryptoKey | null): Promise<CryptoKey> {
  const mixed = deviceSecret ? await subtle().sign("HMAC", deviceSecret, bits) : bits;
  return subtle().importKey("raw", mixed, { name: "AES-GCM" }, false, ["wrapKey", "unwrapKey"]);
}

/** KEK from a human secret (PIN or password). */
export async function deriveKek(secret: string, salt: Uint8Array, iterations: number, deviceSecret: CryptoKey | null): Promise<CryptoKey> {
  const base = await subtle().importKey("raw", te.encode(secret.normalize("NFKC")), "PBKDF2", false, ["deriveBits"]);
  const bits = await subtle().deriveBits({ name: "PBKDF2", hash: "SHA-256", salt: buf(salt), iterations }, base, 256);
  return kekFromBits(bits, deviceSecret);
}

/** KEK from a high-entropy machine secret (server-held device key): no stretching needed. */
export async function kekFromMachineSecret(secret: Uint8Array, deviceSecret: CryptoKey | null): Promise<CryptoKey> {
  const bits = await subtle().digest("SHA-256", buf(secret));
  return kekFromBits(bits, deviceSecret);
}

/** A new data key. Extractable only so that it can be wrapped into further slots while unlocked. */
export async function generateDataKey(): Promise<CryptoKey> {
  return subtle().generateKey({ name: "AES-GCM", length: 256 }, true, ["encrypt", "decrypt"]);
}

export interface WrappedKey {
  iv: Uint8Array;
  wrapped: ArrayBuffer;
}

export async function wrapDataKey(dek: CryptoKey, kek: CryptoKey): Promise<WrappedKey> {
  const iv = randomBytes(12);
  const wrapped = await subtle().wrapKey("raw", dek, kek, { name: "AES-GCM", iv });
  return { iv, wrapped };
}

/** Throws WrongSecretError when the KEK does not match (GCM authentication fails). */
export async function unwrapDataKey(w: WrappedKey, kek: CryptoKey): Promise<CryptoKey> {
  try {
    return await subtle().unwrapKey("raw", w.wrapped, kek, { name: "AES-GCM", iv: buf(w.iv) }, { name: "AES-GCM", length: 256 }, true, ["encrypt", "decrypt"]);
  } catch {
    throw new WrongSecretError();
  }
}

export interface Box {
  iv: Uint8Array;
  ct: ArrayBuffer;
}

export async function encryptJson(key: CryptoKey, value: unknown, aad: string): Promise<Box> {
  const iv = randomBytes(12);
  const ct = await subtle().encrypt({ name: "AES-GCM", iv, additionalData: te.encode(aad) }, key, te.encode(JSON.stringify(value)));
  return { iv, ct };
}

export async function decryptJson<T>(key: CryptoKey, box: Box, aad: string): Promise<T> {
  try {
    const pt = await subtle().decrypt({ name: "AES-GCM", iv: buf(box.iv), additionalData: te.encode(aad) }, key, box.ct);
    return JSON.parse(td.decode(pt)) as T;
  } catch {
    throw new WrongSecretError();
  }
}

/* ───────────── encrypted export file (OFF-20) ─────────────
 * {
 *   "format": "btn-offline-export", "v": 1,
 *   "kdf":    { "name": "PBKDF2", "hash": "SHA-256", "iterations": 310000, "salt": "<b64 16 bytes>" },
 *   "cipher": { "name": "AES-GCM", "iv": "<b64 12 bytes>", "aad": "btn-offline-export/v1" },
 *   "ct":     "<b64 ciphertext of the UTF-8 JSON body>"
 * }
 * The JSON body is { exportedAt, userId, deviceId, records: [{ uuid, type, schemaVersion, seq, revision, capturedAt, payload }] }.
 */
export const EXPORT_FORMAT = "btn-offline-export";
const EXPORT_AAD = "btn-offline-export/v1";

export interface ExportFile {
  format: typeof EXPORT_FORMAT;
  v: 1;
  kdf: { name: "PBKDF2"; hash: "SHA-256"; iterations: number; salt: string };
  cipher: { name: "AES-GCM"; iv: string; aad: string };
  ct: string;
}

async function passphraseKey(passphrase: string, salt: Uint8Array, iterations: number, usage: KeyUsage) {
  const base = await subtle().importKey("raw", te.encode(passphrase.normalize("NFKC")), "PBKDF2", false, ["deriveKey"]);
  return subtle().deriveKey({ name: "PBKDF2", hash: "SHA-256", salt: buf(salt), iterations }, base, { name: "AES-GCM", length: 256 }, false, [usage]);
}

export async function encryptExport(passphrase: string, body: unknown, iterations = PBKDF2_ITERATIONS): Promise<ExportFile> {
  const salt = randomBytes(16);
  const iv = randomBytes(12);
  const key = await passphraseKey(passphrase, salt, iterations, "encrypt");
  const ct = await subtle().encrypt({ name: "AES-GCM", iv, additionalData: te.encode(EXPORT_AAD) }, key, te.encode(JSON.stringify(body)));
  return { format: EXPORT_FORMAT, v: 1, kdf: { name: "PBKDF2", hash: "SHA-256", iterations, salt: toB64(salt) }, cipher: { name: "AES-GCM", iv: toB64(iv), aad: EXPORT_AAD }, ct: toB64(ct) };
}

export async function decryptExport<T>(passphrase: string, file: unknown): Promise<T> {
  const f = file as ExportFile;
  if (!f || f.format !== EXPORT_FORMAT || f.v !== 1 || f.kdf?.name !== "PBKDF2" || f.cipher?.name !== "AES-GCM") throw new Error("format");
  const key = await passphraseKey(passphrase, fromB64(f.kdf.salt), f.kdf.iterations, "decrypt");
  try {
    const pt = await subtle().decrypt({ name: "AES-GCM", iv: fromB64(f.cipher.iv), additionalData: te.encode(f.cipher.aad) }, key, fromB64(f.ct));
    return JSON.parse(td.decode(pt)) as T;
  } catch {
    throw new WrongSecretError();
  }
}
