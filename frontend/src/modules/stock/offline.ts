/**
 * Site stock operations as an offline record type (STK-05 / OFF-18): receipts, issues, consumption and adjustment
 * requests of raw materials or finished goods. Server handler: backend/internal/stock/sync.go ("stock_movement").
 * Offline issues that take a balance below zero are accepted by the server and flagged NEGATIVE_STOCK for review.
 */
import { tr } from "@/i18n";
import { formatDate, todayISO } from "@/lib/dates";
import { registerOfflineRecordType } from "@/offline/core/registry";
import type { ItemKind, StockOpInput } from "./p2api";

export const STOCK_OP_TYPE = "stock_movement";

/** Same quantity rules as the server (stock.qtyProblem). */
export function stockQtyProblem(kind: ItemKind, q: number, positive = true): string | null {
  if (!Number.isFinite(q)) return tr("stock.ops.v.qty");
  if (positive && q <= 0) return tr("stock.ops.v.positive");
  if (!positive && q === 0) return tr("stock.ops.v.zero");
  if (Math.abs(q) > 99_999_999) return tr("stock.ops.v.large");
  if (kind === "product" && !Number.isInteger(q)) return tr("stock.ops.v.whole");
  if (Math.abs(q * 1000 - Math.round(q * 1000)) > 1e-6) return tr("stock.ops.v.decimals");
  return null;
}

export function validateStockOp(p: StockOpInput): string | null {
  if (!p.plantId || !p.itemId) return tr("stock.ops.v.required");
  if (!/^\d{4}-\d{2}-\d{2}$/.test(p.date) || p.date > todayISO()) return tr("stock.ops.v.date");
  if (p.op === "consumption" && p.itemKind !== "material") return tr("stock.ops.v.consumption");
  const q = stockQtyProblem(p.itemKind, p.qty, p.op !== "adjustment_request");
  if (q) return q;
  if (p.op !== "receipt" && (p.note ?? "").trim().length < 3) return tr("stock.ops.v.reason");
  if ((p.lotRef ?? "").length > 60) return tr("stock.ops.v.lot");
  return null;
}

registerOfflineRecordType<StockOpInput>({
  type: STOCK_OP_TYPE,
  schemaVersion: 1,
  permission: ["stock.record", "materials.record", "stock.adjust", "materials.adjust"],
  date: (p) => p.date,
  validate: validateStockOp,
  describe: (p) => `${tr(`stock.ops.op.${p.op}`)} · ${formatDate(p.date)} · ${p.qty}${p.lotRef ? ` · ${p.lotRef}` : ""}`,
  fields: [
    { name: "qty", kind: "number", label: () => tr("stock.ops.field.qty") },
    { name: "date", kind: "date", label: () => tr("stock.ops.field.date") },
    { name: "lotRef", kind: "text", label: () => tr("stock.ops.field.lot") },
    { name: "note", kind: "text", label: () => tr("stock.ops.field.note") },
  ],
  invalidate: [["stock"], ["materials"], ["dashboard"]],
});
