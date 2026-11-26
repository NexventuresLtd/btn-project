import type { ReactNode } from "react";
import { Badge, type Tone } from "@/components/ui";
import { cn } from "@/lib/cn";
import { useT } from "@/i18n";
import type { MovementType } from "./api";

const qtyFmt = new Intl.NumberFormat("en-US", { maximumFractionDigits: 3 });

/** Material quantities carry up to 3 decimals (e.g. 12.25 LITER). */
export const fmtQty = (n: number | null | undefined) => qtyFmt.format(Number(n ?? 0));
/** Dash for zero, like the Excel sheet. */
export const fmtQtyDash = (n: number) => (n === 0 ? "–" : qtyFmt.format(n));
export const fmtSigned = (n: number) => (n === 0 ? "–" : `${n > 0 ? "+" : "−"}${qtyFmt.format(Math.abs(n))}`);

/** Labels live in the catalog under materials.type.<TYPE>. */
export const TYPE_META: Record<MovementType, { tone: Tone }> = {
  OPENING: { tone: "navy" },
  RECEIPT: { tone: "green" },
  TRANSFER_IN: { tone: "blue" },
  TRANSFER_OUT: { tone: "purple" },
  CONSUMPTION: { tone: "amber" },
  ISSUE: { tone: "amber" },
  ADJUSTMENT: { tone: "slate" },
};

export const MOVEMENT_TYPES = Object.keys(TYPE_META) as MovementType[];

export function MovementTypeBadge({ type }: { type: MovementType }) {
  const { tx } = useT();
  return <Badge tone={TYPE_META[type].tone}>{tx(`materials.type.${type}`)}</Badge>;
}

export function LowBadge({ className }: { className?: string }) {
  const { t } = useT();
  return <Badge tone="red" dot className={className}>{t("materials.low")}</Badge>;
}

/** Signed ledger quantity: green for stock in, red for stock out. */
export function SignedQty({ value, unit }: { value: number; unit?: string }) {
  return (
    <span className={cn("tabular font-semibold whitespace-nowrap", value > 0 ? "text-emerald-700" : value < 0 ? "text-red-600" : "text-slate-500")}>
      {value > 0 ? "+" : value < 0 ? "−" : ""}
      {fmtQty(Math.abs(value))}
      {unit ? <span className="ml-1 text-[11px] font-medium text-slate-400">{unit}</span> : null}
    </span>
  );
}

/** Header cell / body cell for the module's grouped tables (same look as DataTable). */
export function Th({ children, right, className }: { children?: ReactNode; right?: boolean; className?: string }) {
  return <th className={cn("px-4 py-2.5 text-[11px] font-semibold tracking-wide whitespace-nowrap text-slate-500 uppercase", right ? "text-right" : "text-left", className)}>{children}</th>;
}

export function Td({ children, right, className }: { children?: ReactNode; right?: boolean; className?: string }) {
  return <td className={cn("px-4 py-2.5 align-middle text-slate-700", right && "tabular text-right whitespace-nowrap", className)}>{children}</td>;
}
