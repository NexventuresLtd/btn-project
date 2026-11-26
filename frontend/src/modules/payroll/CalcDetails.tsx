import { TriangleAlert } from "lucide-react";
import { Badge } from "@/components/ui";
import { useT } from "@/i18n";
import { formatInt, formatQty } from "@/lib/format";
import { formatDate } from "@/lib/dates";
import type { PeriodLine } from "./api";

const signed = (n: number) => (n > 0 ? `+${formatInt(n)}` : n < 0 ? `−${formatInt(-n)}` : "–");

/** Compact list of the calculation inputs on a wage line (WRK-03): days × rate, half days × rate, overtime × rate, piece, adjustments. */
export function CalcSummary({ line: l }: { line: PeriodLine }) {
  const { t } = useT();
  const parts: string[] = [];
  if (l.fullDays) parts.push(t("payroll.wrk.sumDays", { n: formatInt(l.fullDays), rate: formatInt(l.dailyWage) }));
  if (l.halfDays) parts.push(t("payroll.wrk.sumHalf", { n: formatInt(l.halfDays), rate: formatInt(l.halfDayRate) }));
  if (l.overtimeHours) parts.push(t("payroll.wrk.sumOvertime", { n: formatQty(l.overtimeHours), rate: formatInt(l.overtimeRate) }));
  if (l.pieceUnits) parts.push(t("payroll.wrk.sumPiece", { n: formatInt(l.pieceUnits), amount: formatInt(l.pieceAmount) }));
  if (l.adjustmentAmount) parts.push(t("payroll.wrk.sumAdjust", { amount: signed(l.adjustmentAmount) }));
  if (!parts.length) return null;
  return <p className="text-xs text-slate-500">{parts.join(" · ")}</p>;
}

/** Badges for an override (reason + the system figure) and calculation warnings. */
export function LineFlags({ line: l }: { line: PeriodLine }) {
  const { t, tx } = useT();
  const warnings = l.calculation?.warnings ?? [];
  return (
    <>
      {l.isOverride ? (
        <p className="mt-1 flex flex-wrap items-center gap-1.5 text-xs text-amber-800">
          <Badge tone="amber">{t("payroll.wrk.override")}</Badge>
          <span>{t("payroll.wrk.overrideLine", { reason: l.overrideReason ?? "", amount: formatInt(l.calculatedGross ?? 0) })}</span>
        </p>
      ) : null}
      {warnings.map((w) => (
        <p key={w} className="mt-0.5 flex items-center gap-1 text-xs font-medium text-amber-700">
          <TriangleAlert className="size-3 shrink-0" /> {tx(`payroll.warn.${w.split(".").pop()}`)}
        </p>
      ))}
    </>
  );
}

/** Expanded row: rate segments, piece work and adjustments behind one wage line. */
export function CalcDetails({ line: l }: { line: PeriodLine }) {
  const { t, tx } = useT();
  const c = l.calculation;
  if (!c) return <p className="text-xs text-slate-500">{t("payroll.wrk.notCalculated")}</p>;
  return (
    <div className="grid gap-4 text-xs lg:grid-cols-3">
      <section>
        <h4 className="mb-1.5 font-semibold text-slate-700">{t("payroll.wrk.segments")} · {tx(`payroll.wrk.basis.${c.rateBasis}`)}</h4>
        {c.segments.length ? (
          <ul className="space-y-1.5">
            {c.segments.map((s, i) => (
              <li key={i} className="rounded-lg border border-line bg-white px-2.5 py-2">
                <p className="font-medium text-slate-800">{s.ruleName} <span className="font-normal text-slate-500">· {s.from === s.to ? formatDate(s.from) : `${formatDate(s.from)} – ${formatDate(s.to)}`}</span></p>
                <p className="tabular text-slate-600">
                  {t("payroll.wrk.segLine", { full: s.fullDays, daily: formatInt(s.dailyRate), half: s.halfDays, halfRate: formatInt(s.halfDayRate), hours: formatQty(s.overtimeHours), otRate: formatInt(s.overtimeRate) })}
                </p>
                <p className="tabular font-semibold text-slate-800">= {formatInt(s.basic + s.overtime)}</p>
              </li>
            ))}
          </ul>
        ) : (
          <p className="text-slate-500">{t("payroll.wrk.noDays")}</p>
        )}
      </section>
      <section>
        <h4 className="mb-1.5 font-semibold text-slate-700">{t("payroll.wrk.pieces")}</h4>
        {c.pieces.length ? (
          <ul className="space-y-1">
            {c.pieces.map((p, i) => (
              <li key={i} className="tabular flex justify-between gap-2 text-slate-600">
                <span>{p.productCode} {p.productName}: {formatInt(p.units)} × {formatInt(p.rate)}</span>
                <span className="font-semibold text-slate-800">{formatInt(p.amount)}</span>
              </li>
            ))}
          </ul>
        ) : (
          <p className="text-slate-500">—</p>
        )}
      </section>
      <section>
        <h4 className="mb-1.5 font-semibold text-slate-700">{t("payroll.wrk.adjustments")}</h4>
        {c.adjustments.length ? (
          <ul className="space-y-1">
            {c.adjustments.map((a) => (
              <li key={a.id} className="text-slate-600">
                <span className="tabular font-semibold text-slate-800">{signed(a.amount)}</span> · {formatDate(a.date)} · {a.reason}
                {a.sourcePeriod ? <span className="text-slate-500"> ({t("payroll.wrk.fromSheet", { number: a.sourcePeriod })})</span> : null}
              </li>
            ))}
          </ul>
        ) : (
          <p className="text-slate-500">—</p>
        )}
        {c.exceptions ? <p className="mt-2 text-slate-600">{t("payroll.wrk.exceptions", { count: c.exceptions })}</p> : null}
      </section>
    </div>
  );
}
