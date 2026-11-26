import { Badge, type Tone } from "@/components/ui";
import { useLookups } from "@/hooks/useLookups";
import { useT } from "@/i18n";
import type { CountStatus, StockCount } from "./api";

const TONE: Record<CountStatus, Tone> = { OPEN: "blue", SUBMITTED: "amber", APPROVED: "green", CANCELLED: "red" };

export function CountStatusBadge({ status }: { status: string }) {
  const { tx } = useT();
  return <Badge tone={TONE[status as CountStatus] ?? "slate"} dot>{tx(`stockcount.status.${status}`, undefined, status)}</Badge>;
}

/** Category name from reference data (product_category / material_category), falling back to the code. */
export function useCategoryName() {
  const { data } = useLookups();
  return (kind: string | null | undefined, code: string | null | undefined) => {
    if (!code) return "";
    const list = data?.refdata?.[kind === "product" ? "product_category" : "material_category"] ?? [];
    return list.find((r) => r.code === code)?.name ?? code;
  };
}

/** "Full count" / "By category · Raw materials · Cement" (+ blind). */
export function ScopeText({ c }: { c: Pick<StockCount, "scope" | "itemKind" | "category" | "blind"> }) {
  const { t, tx } = useT();
  const cat = useCategoryName();
  const parts = [tx(`stockcount.scope.${c.scope}`)];
  if (c.itemKind) parts.push(tx(`stockcount.kind.${c.itemKind}`));
  if (c.category) parts.push(cat(c.itemKind, c.category));
  return (
    <span className="inline-flex flex-wrap items-center gap-1.5">
      <span>{parts.join(" · ")}</span>
      {c.blind ? <Badge tone="purple">{t("stockcount.blind")}</Badge> : null}
    </span>
  );
}

export function Info({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <dt className="text-[11px] font-semibold tracking-wide text-slate-500 uppercase">{label}</dt>
      <dd className="mt-1 text-sm text-slate-800">{children}</dd>
    </div>
  );
}
