/**
 * Worker registration as an offline record type (WRK-01/WRK-02): a site supervisor registers a new casual worker on
 * site, online or offline. Payload = the POST /payroll/workers body; the record uuid becomes the worker id on the
 * server (backend/internal/payroll/sync.go), so the worker can be marked present on this device before the sync.
 * The server stores the registration PENDING until Finance approves it; it stays off wage sheets until then.
 */
import { tr } from "@/i18n";
import { registerOfflineRecordType } from "@/offline/core/registry";
import { getStore } from "@/offline/runtime";
import { normaliseMomo } from "./identity";
import { PAYROLL_KEY, type WorkerPayload } from "./api";

export const WORKER_REGISTRATION_TYPE = "worker_registration";

/** Same rules as the server's parseWorker for a registration; returns a translated message or null. */
export function invalidRegistration(p: WorkerPayload): string | null {
  if (!p.firstName?.trim() || !p.lastName?.trim()) return tr("payroll.worker.err.firstName");
  if (!/^\d{16}$/.test((p.nationalId ?? "").replace(/\s+/g, ""))) return tr("payroll.worker.err.nationalId");
  if (!normaliseMomo(p.momoNumber ?? "")) return tr("payroll.worker.err.momo");
  if (!p.plantId) return tr("payroll.worker.err.plant");
  if (!Number.isInteger(p.dailyWage) || p.dailyWage < 0 || (p.dailyWage === 0 && p.rateBasis !== "PIECE")) return tr("payroll.worker.err.wagePositive");
  return null;
}

/** The site's worker list the offline attendance roster is built from (modules/attendance/offline.ts). */
interface CachedWorkers {
  plant: { id: string; code: string; name: string };
  workers: { workerId: string; name: string; jobTitle: string }[];
}

/** Adds (or renames) a registered worker in the cached site worker list; pure. */
export function withRegisteredWorker(cached: CachedWorkers, worker: { workerId: string; name: string; jobTitle: string }): CachedWorkers {
  const rest = cached.workers.filter((w) => w.workerId !== worker.workerId);
  return { ...cached, workers: [...rest, worker].sort((a, b) => a.name.localeCompare(b.name)) };
}

/** After an offline registration: show the worker on this device's attendance roster straight away. */
export async function addToOfflineRoster(uuid: string, p: WorkerPayload) {
  const store = getStore();
  if (!store) return;
  const key = `attendance:workers:${p.plantId}`;
  const cached = await store.cacheGet<CachedWorkers>(key);
  if (!cached) return;
  const name = `${p.firstName.trim().toUpperCase()} ${p.lastName.trim().toUpperCase()}`;
  await store.cachePut(key, withRegisteredWorker(cached.value, { workerId: uuid, name, jobTitle: p.jobTitle || "Helper" }));
}

registerOfflineRecordType<WorkerPayload>({
  type: WORKER_REGISTRATION_TYPE,
  schemaVersion: 1,
  permission: ["workers.register", "workers.manage"],
  validate: invalidRegistration,
  describe: (p) => tr("payroll.reg.describe", { name: `${p.firstName} ${p.lastName}` }),
  fields: [
    { name: "firstName", kind: "text", label: () => tr("payroll.worker.firstName") },
    { name: "lastName", kind: "text", label: () => tr("payroll.worker.lastName") },
    { name: "nationalId", kind: "text", label: () => tr("payroll.worker.nationalId") },
    { name: "momoNumber", kind: "text", label: () => tr("payroll.worker.momoNumber") },
    { name: "dailyWage", kind: "number", label: () => tr("payroll.worker.dailyWage") },
  ],
  invalidate: [PAYROLL_KEY],
});
