import { useQuery } from "@tanstack/react-query";
import { get, qs } from "@/lib/api";

/** Record types that have a "Money" view (GET /finance/money?entityType=). */
export type MoneyEntityType =
  | "sale" | "customer" | "delivery" | "purchase_request" | "purchase_order" | "goods_receipt" | "supplier_invoice" | "supplier"
  | "payroll_period" | "worker" | "production_report" | "production_day" | "transfer" | "stock_count" | "site" | "petty_cash_float";

/** One income or expense shown on a record's Money panel. */
export interface MoneyItem {
  kind: "income" | "expense";
  id: string;
  number: string;
  date: string;
  description: string;
  category: string;
  source: string;
  sourceLabel: string;
  status: string;
  statusLabel: string;
  amount: number;
  /** part of the totals (income not void; expense approved / payment recorded) */
  counted: boolean;
  via: string;
  link: string;
  plantCode: string;
}

export interface Money {
  entityType: string;
  entityId: string;
  label: string;
  link: string;
  income: MoneyItem[];
  expenses: MoneyItem[];
  incomeTotal: number;
  expenseTotal: number;
  net: number;
  showIncome: boolean;
  showExpenses: boolean;
}

/** Permissions that let a user see a Money panel (same rule as the API). */
export const MONEY_PERMS = ["income.view", "income.record", "expenses.view", "expenses.approve"] as const;

export const moneyParams = (entityType: string, entityId: string) => ({ entityType, entityId });

export function useMoney(entityType: string, entityId: string | undefined, enabled = true) {
  return useQuery({
    queryKey: ["finance", "money", entityType, entityId],
    queryFn: ({ signal }) => get<Money>(`/finance/money${qs(moneyParams(entityType, entityId ?? ""))}`, signal),
    enabled: enabled && !!entityId,
  });
}

/** Links to the expense / income forms, prefilled with a link to this record (the forms read these params). */
export const newExpenseLink = (entityType: string, entityId: string) => `/expenses${qs({ new: 1, linkType: entityType, linkId: entityId })}`;
export const newIncomeLink = (entityType: string, entityId: string) => `/income${qs({ new: 1, linkType: entityType, linkId: entityId })}`;

/** Badge tone for a money item status. */
export function moneyStatusTone(item: Pick<MoneyItem, "status" | "counted">): "green" | "amber" | "red" | "slate" | "navy" | "blue" {
  switch (item.status) {
    case "VOID":
    case "REJECTED":
    case "CANCELLED":
      return "red";
    case "VERIFIED":
    case "APPROVED":
      return "green";
    case "PAID":
      return "navy";
    case "RECORDED":
      return "blue";
    case "SUBMITTED":
      return "amber";
    default:
      return item.counted ? "green" : "slate";
  }
}

/** Sum of the counted items (the server totals are authoritative; used for tests and fallbacks). */
export const countedTotal = (items: Pick<MoneyItem, "amount" | "counted">[]) => items.reduce((s, i) => s + (i.counted ? i.amount : 0), 0);
