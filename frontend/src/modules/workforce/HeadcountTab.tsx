import { useMemo } from "react";
import { Check } from "lucide-react";
import { Alert, Card, CardHeader, ErrorState, ExportMenu, FilterBar, PdfButton, Spinner, useFilters } from "@/components/ui";
import { useT } from "@/i18n";
import { formatDate, shiftISO, todayISO } from "@/lib/dates";
import { formatInt, formatQty } from "@/lib/format";
import { spanDays, useHeadcount, type HeadcountRow } from "./api";

const MAX_DAYS = 62;

/** WRK-02: daily headcount per site (present + half day), screen + PDF + CSV/Excel. */
export function HeadcountTab() {
  const { t } = useT();
  const { filters, set, pinnedPlant } = useFilters({ from: shiftISO(todayISO(), -13) });
  const tooLong = spanDays(filters.from, filters.to) > MAX_DAYS;
  const q = useHeadcount(filters.from, filters.to, filters.plantId, !tooLong);
  const d = q.data;
  const params = { from: filters.from, to: filters.to, plantId: filters.plantId || undefined };

  // pivot: one row per day, one column per site
  const grid = useMemo(() => {
    const byDate = new Map<string, Map<string, HeadcountRow>>();
    for (const r of d?.rows ?? []) {
      if (!byDate.has(r.date)) byDate.set(r.date, new Map());
      byDate.get(r.date)!.set(r.plant.id, r);
    }
    const siteTotals = new Map<string, number>();
    const days = [...byDate.entries()].sort(([a], [b]) => b.localeCompare(a)).map(([date, m]) => {
      let total = 0;
      let ot = 0;
      for (const r of m.values()) {
        total += r.headcount;
        ot += r.overtimeHours;
        siteTotals.set(r.plant.id, (siteTotals.get(r.plant.id) ?? 0) + r.headcount);
      }
      return { date, m, total, ot };
    });
    return { days, siteTotals };
  }, [d]);

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <FilterBar filters={filters} onChange={set} pinnedPlant={pinnedPlant} onlySites />
        <div className="flex flex-wrap gap-2">
          <ExportMenu path="/workforce/reports/headcount" params={params} />
          <PdfButton path="/workforce/pdf/headcount" params={params} />
        </div>
      </div>
      {tooLong ? <Alert tone="amber">{t("workforce.rangeMax", { n: MAX_DAYS })}</Alert> : null}
      <Card>
        <CardHeader title={t("workforce.headcount.title")} description={t("workforce.headcount.hint")} />
        {q.isError ? (
          <ErrorState error={q.error} retry={() => q.refetch()} />
        ) : q.isLoading || !d ? (
          <div className="flex justify-center py-14"><Spinner /></div>
        ) : !grid.days.length ? (
          <p className="px-5 py-12 text-center text-sm text-slate-500">{t("workforce.empty")}</p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <caption className="sr-only">{t("workforce.headcount.title")}</caption>
              <thead>
                <tr className="border-y border-line bg-slate-50/70 text-[11px] font-semibold tracking-wide whitespace-nowrap text-slate-500 uppercase">
                  <th scope="col" className="px-4 py-2.5 text-left">{t("workforce.col.date")}</th>
                  {d.sites.map((s) => <th key={s.id} scope="col" className="px-4 py-2.5 text-right">{s.code}</th>)}
                  <th scope="col" className="px-4 py-2.5 text-right">{t("workforce.col.total")}</th>
                  <th scope="col" className="px-4 py-2.5 text-right">{t("workforce.col.overtimeHours")}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-line">
                {grid.days.map(({ date, m, total, ot }) => (
                  <tr key={date}>
                    <th scope="row" className="px-4 py-2.5 text-left font-medium whitespace-nowrap text-slate-900">{formatDate(date)}</th>
                    {d.sites.map((s) => {
                      const r = m.get(s.id);
                      return (
                        <td key={s.id} className="tabular px-4 py-2.5 text-right text-slate-700">
                          <span className="inline-flex items-center justify-end gap-1">
                            {formatInt(r?.headcount ?? 0)}
                            {r?.dayStatus === "APPROVED" ? <Check className="size-3.5 text-emerald-600" aria-label={t("workforce.day.APPROVED")} /> : null}
                          </span>
                          {r && r.unmarked > 0 ? <span className="block text-[11px] text-amber-700">{t("workforce.headcount.unmarked", { n: r.unmarked })}</span> : null}
                        </td>
                      );
                    })}
                    <td className="tabular px-4 py-2.5 text-right font-semibold text-slate-900">{formatInt(total)}</td>
                    <td className="tabular px-4 py-2.5 text-right text-slate-700">{formatQty(ot)}</td>
                  </tr>
                ))}
              </tbody>
              <tfoot className="border-t-2 border-navy-100 bg-navy-50/50 font-semibold text-slate-900">
                <tr>
                  <th scope="row" className="px-4 py-2.5 text-left">{t("workforce.col.total")}</th>
                  {d.sites.map((s) => <td key={s.id} className="tabular px-4 py-2.5 text-right">{formatInt(grid.siteTotals.get(s.id) ?? 0)}</td>)}
                  <td className="tabular px-4 py-2.5 text-right">{formatInt(d.totals.headcount)}</td>
                  <td className="tabular px-4 py-2.5 text-right">{formatQty(d.totals.overtimeHours)}</td>
                </tr>
              </tfoot>
            </table>
          </div>
        )}
      </Card>
    </div>
  );
}
