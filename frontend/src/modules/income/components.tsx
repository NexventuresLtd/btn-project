import { HandCoins, ShoppingCart } from "lucide-react";
import { Badge, type Tone } from "@/components/ui";
import { useT } from "@/i18n";
import type { IncomeSource, IncomeStatus } from "./api";

const STATUS_TONE: Record<IncomeStatus, Tone> = { RECORDED: "blue", VERIFIED: "green", VOID: "red" };

/** Income status: Recorded → Verified (finance) | Void (never deleted). */
export function IncomeStatusBadge({ status, className }: { status: IncomeStatus; className?: string }) {
  const { tx } = useT();
  return <Badge tone={STATUS_TONE[status] ?? "slate"} dot className={className}>{tx(`income.status.${status}`)}</Badge>;
}

/** Where the income comes from: a payment recorded on an order, or other income recorded here. */
export function SourceBadge({ source }: { source: IncomeSource }) {
  const { tx } = useT();
  const Icon = source === "ORDER_PAYMENT" ? ShoppingCart : HandCoins;
  return <Badge tone={source === "ORDER_PAYMENT" ? "purple" : "slate"}><Icon className="size-3" aria-hidden />{tx(`income.source.${source}`)}</Badge>;
}
