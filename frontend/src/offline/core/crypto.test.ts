import { describe, expect, it } from "vitest";
import "fake-indexeddb/auto";
import { PBKDF2_ITERATIONS, WrongSecretError, createDeviceSecret, decryptExport, decryptJson, deriveKek, encryptExport, encryptJson, generateDataKey, randomBytes, unwrapDataKey, wrapDataKey } from "./crypto";
import { UserDB } from "./db";
import { MAX_PIN_FAILURES, PinLockedError, Vault, validatePin } from "./vault";

describe("crypto primitives", () => {
  it("round-trips JSON with AES-GCM and rejects a different AAD", async () => {
    const k = await generateDataKey();
    const box = await encryptJson(k, { a: 1, s: "ሰላም" }, "u1|outbox|x");
    expect(new Uint8Array(box.ct).length).toBeGreaterThan(16);
    expect(await decryptJson(k, box, "u1|outbox|x")).toEqual({ a: 1, s: "ሰላም" });
    await expect(decryptJson(k, box, "u2|outbox|x")).rejects.toBeInstanceOf(WrongSecretError);
  });

  it("wraps the data key with a PIN-derived KEK; a wrong PIN or another device fails", async () => {
    const dev = await createDeviceSecret();
    const other = await createDeviceSecret();
    const salt = randomBytes(16);
    const dek = await generateDataKey();
    const box = await encryptJson(dek, "secret", "aad");
    const w = await wrapDataKey(dek, await deriveKek("482916", salt, PBKDF2_ITERATIONS, dev));
    const back = await unwrapDataKey(w, await deriveKek("482916", salt, PBKDF2_ITERATIONS, dev));
    expect(await decryptJson(back, box, "aad")).toBe("secret");
    await expect(unwrapDataKey(w, await deriveKek("482917", salt, PBKDF2_ITERATIONS, dev))).rejects.toBeInstanceOf(WrongSecretError);
    await expect(unwrapDataKey(w, await deriveKek("482916", salt, PBKDF2_ITERATIONS, other))).rejects.toBeInstanceOf(WrongSecretError);
  });

  it("encrypts an export file with a passphrase (documented format)", async () => {
    const body = { records: [{ uuid: "u", payload: { x: 1 } }] };
    const f = await encryptExport("correct horse", body, 1000);
    expect(f.format).toBe("btn-offline-export");
    expect(f.kdf).toMatchObject({ name: "PBKDF2", hash: "SHA-256", iterations: 1000 });
    expect(JSON.stringify(f)).not.toContain("payload");
    expect(await decryptExport("correct horse", f)).toEqual(body);
    await expect(decryptExport("wrong", f)).rejects.toBeInstanceOf(WrongSecretError);
    await expect(decryptExport("x", { format: "nope" })).rejects.toThrow("format");
  });
});

describe("vault", () => {
  const make = async (name: string) => {
    const dev = await createDeviceSecret();
    const db = new UserDB(name);
    return { dev, db, vault: new Vault(db, "user-1", dev, 2000) };
  };

  it("validates PINs: ≥ 6 digits, not trivial", () => {
    expect(validatePin("12345")).toBe("length");
    expect(validatePin("12a456")).toBe("digits");
    expect(validatePin("111111")).toBe("weak");
    expect(validatePin("123456")).toBe("weak");
    expect(validatePin("987654")).toBe("weak");
    expect(validatePin("482916")).toBe("ok");
  });

  it("unlocks with PIN, password or session slot; locked vault cannot decrypt", async () => {
    const { dev, db, vault } = await make("vault-a");
    await vault.create();
    const machine = randomBytes(32);
    await vault.addPinSlot("482916");
    await vault.addPasswordSlot("Pass word!");
    await vault.addSessionSlot(machine);
    const box = await vault.encrypt({ n: 42 }, "records|t|k");
    vault.lock();
    await expect(vault.decrypt(box, "records|t|k")).rejects.toThrow("locked");

    const v2 = new Vault(db, "user-1", dev, 2000);
    await v2.unlockWithPin("482916");
    expect(await v2.decrypt(box, "records|t|k")).toEqual({ n: 42 });
    v2.lock();
    await v2.unlockWithPassword("Pass word!");
    v2.lock();
    await v2.unlockWithSession(machine);
    v2.lock();
    await expect(v2.unlockWithSession(randomBytes(32))).rejects.toBeInstanceOf(WrongSecretError);
    // the same database opened with another device secret cannot be unlocked even with the right PIN
    const stranger = new Vault(db, "user-1", await createDeviceSecret(), 2000);
    await expect(stranger.unlockWithPin("482916")).rejects.toBeInstanceOf(WrongSecretError);
    db.close();
  });

  it(`destroys the PIN slot after ${MAX_PIN_FAILURES} wrong PINs but keeps the data (password still works)`, async () => {
    const { db, vault } = await make("vault-b");
    await vault.create();
    await vault.addPinSlot("482916");
    await vault.addPasswordSlot("pw");
    const box = await vault.encrypt("kept", "x");
    vault.lock();
    for (let i = 1; i < MAX_PIN_FAILURES; i++) {
      await expect(vault.unlockWithPin("000001")).rejects.toBeInstanceOf(WrongSecretError);
      expect(await vault.pinFailures()).toBe(i);
    }
    await expect(vault.unlockWithPin("000001")).rejects.toBeInstanceOf(PinLockedError);
    await expect(vault.unlockWithPin("482916")).rejects.toBeInstanceOf(PinLockedError);
    await vault.unlockWithPassword("pw");
    expect(await vault.decrypt(box, "x")).toBe("kept");
    db.close();
  });
});
