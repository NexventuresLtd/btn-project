import type { ReactNode } from "react";
import { ArrowRight } from "lucide-react";
import { Badge, type Tone } from "@/components/ui";
import { useT } from "@/i18n";
import type { LocRef, TransferStatus } from "./api";

const TONE: Record<TransferStatus, Tone> = {
  REQUESTED: "amber", APPROVED: "blue", IN_TRANSIT: "purple", RECEIVED: "green", REJECTED: "red", CANCELLED: "slate",
};

export function TransferStatusBadge({ status, reversed }: { status: TransferStatus; reversed?: boolean }) {
  const { tx } = useT();
  return (
    <Badge tone={TONE[status] ?? "slate"} dot>
      {tx(`transfers.status.${status}`, undefined, status)}{reversed ? ` · ${tx("transfers.reversed")}` : ""}
    </Badge>
  );
}

/** Location code with its kind (site / head office / warehouse). */
export function LocationLabel({ loc, className }: { loc: Pick<LocRef, "code" | "name"> & { kind?: string }; className?: string }) {
  const { tx } = useT();
  return (
    <span className={className}>
      <span className="font-medium text-slate-800">{loc.code}</span>
      {loc.kind && loc.kind !== "SITE" ? <span className="ml-1 text-[11px] font-semibold tracking-wide text-slate-500 uppercase">{tx(`admin.sites.kind.${loc.kind}`, undefined, loc.kind)}</span> : null}
    </span>
  );
}

export function Route({ from, to }: { from: LocRef; to: LocRef }) {
  const { t } = useT();
  return (
    <span className="inline-flex flex-wrap items-center gap-1.5 whitespace-nowrap">
      <LocationLabel loc={from} />
      <ArrowRight className="size-3.5 text-slate-400" aria-label={t("transfers.to")} />
      <LocationLabel loc={to} />
    </span>
  );
}

export function Info({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-[11px] font-semibold tracking-wide text-slate-500 uppercase">{label}</dt>
      <dd className="mt-1 text-sm text-slate-800">{children}</dd>
    </div>
  );
}

export const kindLabel = (tx: ReturnType<typeof useT>["tx"], kind: string) => tx(`transfers.kind.${kind}`, undefined, kind);
