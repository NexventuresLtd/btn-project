/**
 * Encrypted export / import of unsynced records (OFF-20). File format: see core/crypto.ts (`ExportFile`).
 * Import queues the records in the current user's outbox with their original uuid, revision and capture
 * time, so they travel through the normal sync API and the server's (uuid, revision) idempotency skips
 * anything that already arrived from the original device.
 */
import { decryptExport, encryptExport, EXPORT_FORMAT, WrongSecretError, type ExportFile } from "./core/crypto";
import { getStore } from "./runtime";

export interface ExportBody {
  exportedAt: string;
  userId: string;
  deviceId: string;
  records: { uuid: string; type: string; schemaVersion: number; seq: number; revision: number; capturedAt: string; status: string; payload: unknown }[];
}

export async function buildExport(passphrase: string): Promise<{ file: ExportFile; count: number }> {
  const s = getStore();
  if (!s) throw new Error("locked");
  const items = [...(await s.pendingItems()), ...(await s.issues())];
  const body: ExportBody = {
    exportedAt: new Date().toISOString(),
    userId: s.userId,
    deviceId: s.deviceId,
    records: items.map((i) => ({ uuid: i.uuid, type: i.type, schemaVersion: i.schemaVersion, seq: i.seq, revision: i.revision, capturedAt: i.capturedAt, status: i.status, payload: i.secret.payload })),
  };
  return { file: await encryptExport(passphrase, body), count: body.records.length };
}

export function downloadJson(name: string, value: unknown) {
  const blob = new Blob([JSON.stringify(value)], { type: "application/json" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 10_000);
}

export type ImportError = "format" | "wrong" | "locked";

export async function importFile(text: string, passphrase: string): Promise<{ ok: true; count: number } | { ok: false; error: ImportError }> {
  const s = getStore();
  if (!s) return { ok: false, error: "locked" };
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    return { ok: false, error: "format" };
  }
  if ((parsed as ExportFile)?.format !== EXPORT_FORMAT) return { ok: false, error: "format" };
  let body: ExportBody;
  try {
    body = await decryptExport<ExportBody>(passphrase, parsed);
  } catch (e) {
    return { ok: false, error: e instanceof WrongSecretError ? "wrong" : "format" };
  }
  let count = 0;
  for (const r of [...body.records].sort((a, b) => a.seq - b.seq)) {
    if (await s.db.outbox.get(r.uuid)) continue; // already queued here
    const item = await s.capture({ type: r.type, payload: r.payload, uuid: r.uuid, capturedAt: r.capturedAt });
    if (r.revision > 1) await s.db.outbox.update(item.uuid, { revision: r.revision });
    count++;
  }
  return { ok: true, count };
}
