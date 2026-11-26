import { Banknote, Briefcase, ShoppingCart, Users, Wallet } from "lucide-react";
import { Badge, StatusBadge, type Tone } from "@/components/ui";
import { useT } from "@/i18n";
import type { ExpenseOrigin, ExpenseStatus } from "./api";

/** Expense status; PAID reads "Payment recorded" (the system records payments made outside it, it never pays). */
export function ExpenseStatusBadge({ status, className }: { status: ExpenseStatus; className?: string }) {
  const { tx } = useT();
  if (status === "PAID") return <Badge tone="navy" dot className={className}>{tx("expenses.status.PAID")}</Badge>;
  return <StatusBadge status={status} className={className} />;
}

const ORIGIN_TONE: Record<ExpenseOrigin, Tone> = { DIRECT: "slate", PETTY_CASH: "green", PROCUREMENT: "purple", PAYROLL: "blue", OTHER: "slate" };
const ORIGIN_ICON = { DIRECT: Banknote, PETTY_CASH: Wallet, PROCUREMENT: ShoppingCart, PAYROLL: Users, OTHER: Briefcase } as const;

/** Where the expense comes from: direct, petty cash, purchase, wage sheet, other. */
export function OriginBadge({ origin }: { origin: ExpenseOrigin }) {
  const { tx } = useT();
  const Icon = ORIGIN_ICON[origin] ?? Banknote;
  return <Badge tone={ORIGIN_TONE[origin] ?? "slate"}><Icon className="size-3" aria-hidden />{tx(`expenses.origin.${origin}`)}</Badge>;
}
