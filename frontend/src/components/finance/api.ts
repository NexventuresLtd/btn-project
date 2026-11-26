/**
 * Shared finance-record API (docs/FINANCE_RECORDS_DESIGN.md): budget line options, the FIN-08 budget check of an
 * expense, and the "linked records" of an expense or income. Used by BudgetLinePicker and LinksEditor; query keys
 * start with "finance" so every finance mutation that invalidates ["finance"] refreshes them.
 */
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { api, get, post, qs } from "@/lib/api";

export type BudgetLineType = "EXPENSE" | "INCOME";

/** One line of an approved budget in force on the date (GET /budgets/line-options). */
export interface LineOption {
  lineId: string;
  type: BudgetLineType;
  budgetId: string;
  budgetNumber: string;
  budgetName: string;
  department: string;
  departmentName: string;
  category: string;
  categoryName: string;
  periodType: string;
  periodStart: string;
  periodEnd: string;
  /** approved amount in force */
  amount: number;
  /** spent (expense lines) / received (income lines) */
  actual: number;
  /** committed, awaiting approval (expense lines) */
  pending: number;
  /** amount − actual − pending */
  remaining: number;
  suggested: boolean;
}

export interface LineOptions {
  type: BudgetLineType;
  date: string;
  items: LineOption[];
  suggested: LineOption | null;
}

export type BudgetMode = "suggested" | "chosen" | "unbudgeted" | "none" | "no_line";

/** POST /expenses/budget-check → the FIN-08 position of a planned expense on the line it would use. */
export interface ExpenseBudgetCheck {
  hasBudget: boolean;
  budgetId?: string;
  budgetNumber?: string;
  budgetName?: string;
  periodType?: string;
  periodStart?: string;
  periodEnd?: string;
  department: string;
  category: string;
  budgeted: boolean;
  lineId?: string;
  mode: BudgetMode;
  check: { budget: number; spent: number; pending: number; remaining: number; amount: number; wouldExceed: boolean; overrunAmount: number };
  levels: { level: number; permission: string; role: string | null; label: string; minAmount: number; maxAmount: number | null }[];
}

export interface BudgetCheckInput {
  category: string;
  department: string;
  date: string;
  amount: number;
  expenseId?: string | null;
  budgetLineId?: string | null;
  unbudgeted?: boolean;
}

/** Entity types a money record can be linked to (backend finlink.Types, in picker order). */
export const LINK_TYPES = [
  "sale", "customer", "delivery", "purchase_request", "purchase_order", "goods_receipt", "supplier_invoice", "supplier",
  "payroll_period", "worker", "production_report", "production_day", "transfer", "stock_count", "site", "petty_cash_float", "reference",
] as const;
export type LinkType = (typeof LINK_TYPES)[number];

/** A stored link of an expense / income (detail JSON `links`, GET …/links). */
export interface MoneyLink {
  id: string;
  entityType: LinkType | string;
  typeLabel: string;
  entityId: string | null;
  label: string;
  note: string | null;
  /** created by the system (purchase, wage sheet, petty cash), cannot be removed */
  auto: boolean;
  /** app path of the linked record, "" when there is none */
  link?: string;
  createdBy: string;
  createdAt: string;
  removedAt?: string | null;
}

/** A record found by GET /finance/link-targets. */
export interface LinkTarget {
  type: LinkType;
  id: string;
  label: string;
  sub?: string;
  link?: string;
}

/** Body of one link: an entity (type + id) or a free reference (type "reference" + text). */
export interface LinkInput {
  entityType: LinkType;
  entityId?: string;
  reference?: string;
  note?: string | null;
}

/** A link collected in a create form before the record exists (LinkPickerList). `label` is for display only. */
export interface DraftLink extends LinkInput {
  label: string;
}

export type LinkBasePath = "/expenses" | "/income";

export const FIN_RECORDS_KEY = ["finance", "records"] as const;

export function useLineOptions(p: { type: BudgetLineType; date: string; department?: string; category?: string }, enabled = true) {
  const query = qs({ type: p.type, date: p.date, department: p.department || undefined, category: p.category || undefined });
  return useQuery({
    queryKey: [...FIN_RECORDS_KEY, "line-options", query],
    queryFn: ({ signal }) => get<LineOptions>(`/budgets/line-options${query}`, signal),
    enabled: enabled && /^\d{4}-\d{2}-\d{2}$/.test(p.date),
    staleTime: 30_000,
    placeholderData: keepPreviousData,
  });
}

export const expenseBudgetCheck = (b: BudgetCheckInput) => post<ExpenseBudgetCheck>("/expenses/budget-check", b as unknown as Record<string, unknown>);

export function useExpenseBudgetCheck(b: BudgetCheckInput, enabled = true) {
  return useQuery({
    queryKey: [...FIN_RECORDS_KEY, "budget-check", b],
    queryFn: () => expenseBudgetCheck(b),
    enabled: enabled && !!b.category && !!b.department && /^\d{4}-\d{2}-\d{2}$/.test(b.date) && Number.isFinite(b.amount) && b.amount > 0,
    staleTime: 15_000,
    placeholderData: keepPreviousData,
    retry: false,
  });
}

export function useLinkTargets(type: LinkType | "", q: string, enabled = true) {
  return useQuery({
    queryKey: [...FIN_RECORDS_KEY, "link-targets", type, q],
    queryFn: ({ signal }) => get<{ type: string; items: LinkTarget[]; types: { type: LinkType; label: string }[] }>(`/finance/link-targets${qs({ type, q })}`, signal),
    enabled: enabled && !!type && type !== "reference",
    staleTime: 30_000,
    placeholderData: keepPreviousData,
  });
}

/** All links of a record including removed ones (shown collapsed). */
export function useRecordLinks(basePath: LinkBasePath, recordId: string, enabled = true) {
  return useQuery({
    queryKey: [...FIN_RECORDS_KEY, "links", basePath, recordId],
    queryFn: ({ signal }) => get<{ items: MoneyLink[] }>(`${basePath}/${recordId}/links?removed=1`, signal),
    enabled: enabled && !!recordId,
  });
}

export const addLink = ({ basePath, recordId, link }: { basePath: LinkBasePath; recordId: string; link: LinkInput }) =>
  post<{ items: MoneyLink[] }>(`${basePath}/${recordId}/links`, toLinkBody(link));

export const removeLink = ({ basePath, recordId, linkId, reason }: { basePath: LinkBasePath; recordId: string; linkId: string; reason?: string | null }) =>
  api<{ items: MoneyLink[] }>(`${basePath}/${recordId}/links/${linkId}`, { method: "DELETE", body: { reason: reason?.trim() || null } });

/** Wire body of one link (drops the display label). */
export function toLinkBody(l: LinkInput): Record<string, unknown> {
  const note = l.note?.trim() || null;
  return l.entityType === "reference" ? { entityType: "reference", reference: (l.reference ?? "").trim(), note } : { entityType: l.entityType, entityId: l.entityId, note };
}

/** Links collected in a create form → the `links` array of POST /expenses or POST /income. */
export const toLinkInputs = (drafts: DraftLink[]): LinkInput[] =>
  drafts.map((d) => (d.entityType === "reference" ? { entityType: "reference", reference: (d.reference ?? "").trim(), note: d.note?.trim() || null } : { entityType: d.entityType, entityId: d.entityId, note: d.note?.trim() || null }));

/** Same key for the same target, so a target is not linked twice. */
export const linkKey = (l: { entityType: string; entityId?: string | null; reference?: string; label?: string }) =>
  l.entityType === "reference" ? `reference|${(l.reference ?? l.label ?? "").trim().toLowerCase()}` : `${l.entityType}|${l.entityId ?? ""}`;

export const MAX_LINKS = 20;

/** Value of a budget link choice (BudgetLinePicker). Neither a line nor unbudgeted = the suggested line is used. */
export interface BudgetLinkValue {
  budgetLineId: string | null;
  unbudgeted: boolean;
  unbudgetedReason: string;
}

export const EMPTY_BUDGET_LINK: BudgetLinkValue = { budgetLineId: null, unbudgeted: false, unbudgetedReason: "" };

/** Client rule mirrored from the server: an unbudgeted record needs a reason of at least 5 characters. Returns an i18n key. */
export function budgetLinkProblem(v: BudgetLinkValue): "finrecords.v.reason" | null {
  return v.unbudgeted && v.unbudgetedReason.trim().length < 5 ? "finrecords.v.reason" : null;
}

/** The fields to send with a POST/PATCH body (expenses, petty cash, income). */
export function budgetLinkBody(v: BudgetLinkValue): { budgetLineId: string | null; unbudgeted: boolean; unbudgetedReason: string | null } {
  return v.unbudgeted
    ? { budgetLineId: null, unbudgeted: true, unbudgetedReason: v.unbudgetedReason.trim() || null }
    : { budgetLineId: v.budgetLineId || null, unbudgeted: false, unbudgetedReason: null };
}

/** Group line options by budget (budget number · department), keeping the server order. */
export function groupLineOptions(items: LineOption[]): { budgetId: string; budgetNumber: string; budgetName: string; departmentName: string; periodStart: string; periodEnd: string; lines: LineOption[] }[] {
  const out = new Map<string, { budgetId: string; budgetNumber: string; budgetName: string; departmentName: string; periodStart: string; periodEnd: string; lines: LineOption[] }>();
  for (const o of items) {
    const g = out.get(o.budgetId) ?? { budgetId: o.budgetId, budgetNumber: o.budgetNumber, budgetName: o.budgetName, departmentName: o.departmentName, periodStart: o.periodStart, periodEnd: o.periodEnd, lines: [] };
    g.lines.push(o);
    out.set(o.budgetId, g);
  }
  return [...out.values()];
}
