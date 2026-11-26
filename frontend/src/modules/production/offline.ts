/**
 * Production as offline record types (SRS §5, Appendix A). Server handlers: backend/internal/production/sync.go.
 *  - production_entry : the whole entry payload (same body as POST /production/reports); the client uuid becomes the entry id.
 *  - note             : { reportId, body } free-text note on an entry (PRD-09).
 *  - exception_flag   : { reportId, code, note, active } exception flag on an entry (PRD-09).
 * Delta pulls: production_entries (recent entries of the user's site), production_days (closed days), production_recipes.
 */
import { tr } from "@/i18n";
import { formatDate, todayISO } from "@/lib/dates";
import { formatInt } from "@/lib/format";
import { registerOfflineRecordType } from "@/offline/core/registry";
import type { ReportPayload, ReportStatus } from "./api";

export const ENTRY_TYPE = "production_entry";
export const NOTE_TYPE = "note";
export const FLAG_TYPE = "exception_flag";

/** Local copy of an entry: the payload as captured (or as pulled), plus server facts when known. */
export type ProductionEntryLocal = ReportPayload & { id?: string; number?: string; status?: ReportStatus; mergeInto?: string };

export interface NotePayload {
  reportId: string;
  body: string;
}

export interface FlagPayload {
  reportId: string;
  code: string;
  note: string | null;
  active: boolean;
}

const ISO = /^\d{4}-\d{2}-\d{2}$/;

/** Same rules as the server's parseReport (cross-field checks included) — returns a translated message or null. */
export function validateEntry(p: ProductionEntryLocal): string | null {
  if (!p.plantId) return tr("production.err.choosePlant");
  if (!ISO.test(p.date) || p.date > todayISO()) return tr("production.err.dateFuture");
  if (p.shift !== "DAY" && p.shift !== "NIGHT") return tr("production.err.shift");
  const lines = p.lines.filter((l) => l.producedQty || l.targetQty || l.sampleQty || l.defects.length);
  if (!lines.length) return tr("production.err.noLines");
  const seen = new Set<string>();
  for (const l of lines) {
    const k = `${l.productId}|${(l.batchRef || `${p.shift}-1`).trim().toLowerCase().replace(/\s+/g, " ")}`;
    if (seen.has(k)) return tr("production.err.productTwice");
    seen.add(k);
    const bad = [l.producedQty, l.targetQty, l.sampleQty, ...l.defects.map((d) => d.qty)].some((n) => !Number.isInteger(n) || n < 0);
    if (bad) return tr("production.err.wholeBlocks");
    if ((l.batchRef ?? "").length > 40) return tr("production.err.batchLong");
    const prodDefects = l.defects.filter((d) => d.reasonCode !== "HANDLING").reduce((s, d) => s + d.qty, 0);
    if (prodDefects + l.sampleQty > l.producedQty) return tr("production.damageExceeds");
  }
  if (p.downtimeMins > 0 && !p.downtimeNote) return tr("production.err.downtimeCause");
  return null;
}

registerOfflineRecordType<ProductionEntryLocal>({
  type: ENTRY_TYPE,
  schemaVersion: 1,
  permission: "production.record",
  date: (p) => p.date,
  validate: validateEntry,
  describe: (p) => {
    const produced = p.lines.reduce((s, l) => s + (l.producedQty || 0), 0);
    return `${formatDate(p.date)} · ${tr(`production.shift.${p.shift}`)} · ${tr("production.nProduced", { n: formatInt(produced) })}${p.submit ? "" : ` · ${tr("enums.status.DRAFT")}`}`;
  },
  fields: [
    { name: "date", kind: "date", label: () => tr("common.date") },
    { name: "shift", kind: "select", label: () => tr("production.shiftLabel"), options: () => (["DAY", "NIGHT"] as const).map((s) => ({ value: s, label: tr(`production.shift.${s}`) })) },
    { name: "downtimeMins", kind: "number", label: () => tr("production.downtimeMinutes") },
    { name: "downtimeNote", kind: "text", label: () => tr("production.downtimeCause") },
    { name: "notes", kind: "text", label: () => tr("common.notes") },
  ],
  invalidate: [["production"], ["stock"], ["materials"], ["dashboard"]],
  pull: {
    entity: "production_entries",
    key: (d) => String(d.id),
    date: (d) => String(d.date ?? ""),
    toLocal: (d) => ({
      id: String(d.id),
      number: String(d.number ?? ""),
      status: d.status as ReportStatus,
      plantId: String(d.plantId),
      date: String(d.date),
      shift: d.shift === "NIGHT" ? "NIGHT" : "DAY",
      lines: ((d.lines as Record<string, unknown>[] | undefined) ?? []).map((l) => ({
        productId: String(l.productId), batchRef: String(l.batchRef ?? ""), additionalBatch: !!l.additionalBatch,
        targetQty: Number(l.targetQty) || 0, producedQty: Number(l.producedQty) || 0, sampleQty: Number(l.sampleQty) || 0,
        defects: ((l.defects as Record<string, unknown>[] | undefined) ?? []).map((x) => ({ reasonCode: String(x.reasonCode), qty: Number(x.qty) || 0, note: typeof x.note === "string" ? x.note : null })),
      })),
      materialUsages: ((d.materialUsages as Record<string, unknown>[] | undefined) ?? []).map((u) => ({ materialId: String(u.materialId), quantity: Number(u.quantity) || 0 })),
      downtimeMins: Number(d.downtimeMins) || 0,
      downtimeNote: typeof d.downtimeNote === "string" ? d.downtimeNote : null,
      notes: typeof d.notes === "string" ? d.notes : null,
      exceptionFlags: ((d.exceptionFlags as Record<string, unknown>[] | undefined) ?? []).map((f) => ({ code: String(f.code), note: typeof f.note === "string" ? f.note : null })),
      submit: d.status === "SUBMITTED" || d.status === "APPROVED",
    }),
  },
});

registerOfflineRecordType<NotePayload>({
  type: NOTE_TYPE,
  schemaVersion: 1,
  permission: ["production.record", "production.approve"],
  validate: (p) => (!p.reportId ? tr("production.err.noEntry") : !p.body.trim() || p.body.length > 2000 ? tr("production.err.noteRequired") : null),
  describe: (p) => `${tr("production.note")}: ${p.body.slice(0, 80)}`,
  fields: [{ name: "body", kind: "text", label: () => tr("production.note") }],
  invalidate: [["production"]],
});

registerOfflineRecordType<FlagPayload>({
  type: FLAG_TYPE,
  schemaVersion: 1,
  permission: ["production.record", "production.approve"],
  validate: (p) => (!p.reportId ? tr("production.err.noEntry") : !p.code ? tr("production.err.flagRequired") : (p.note?.length ?? 0) > 300 ? tr("production.err.noteLong") : null),
  describe: (p) => `${tr("production.exceptionFlag")}: ${p.code}${p.active ? "" : ` (${tr("production.removed")})`}`,
  fields: [{ name: "note", kind: "text", label: () => tr("production.flagNote") }],
  invalidate: [["production"]],
});
