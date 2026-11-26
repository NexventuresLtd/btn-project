import { useEffect, useMemo, useState } from "react";
import { Lock, Save, Undo2 } from "lucide-react";
import { Alert, Button, Card, DateInput, EmptyState, ErrorState, Field, Input, Select, Spinner } from "@/components/ui";
import { useAuth } from "@/auth/AuthProvider";
import { useT } from "@/i18n";
import { sitesOnly, useLookups } from "@/hooks/useLookups";
import { useMutate } from "@/hooks/useMutate";
import { pinnedPlantId } from "@/lib/scope";
import { todayISO } from "@/lib/dates";
import { formatInt, formatRWF } from "@/lib/format";
import { savePieceWork, usePieceDay, WF_KEY } from "./api";

const cellKey = (w: string, p: string) => `${w}|${p}`;

/** WRK-03 piece work: output per worker, day and product at a site (locked days: managers only, with a reason). */
export function PieceWorkTab() {
  const { t, tx } = useT();
  const { user, can } = useAuth();
  const pinned = pinnedPlantId(user);
  const { data: lookups } = useLookups();
  const sites = sitesOnly(lookups?.plants);
  const [plantId, setPlantId] = useState(pinned ?? "");
  const [date, setDate] = useState(todayISO());
  const site = pinned ?? (plantId || sites[0]?.id || "");
  const q = usePieceDay(site, date);
  const d = q.data;

  const initial = useMemo(() => {
    const m: Record<string, string> = {};
    for (const e of d?.entries ?? []) m[cellKey(e.workerId, e.productId)] = String(e.quantity);
    return m;
  }, [d]);
  const [qty, setQty] = useState<Record<string, string>>({});
  const [reason, setReason] = useState("");
  useEffect(() => { setQty(initial); setReason(""); }, [initial]);

  // products shown: those with a rate today or already used
  const products = useMemo(() => (d?.products ?? []).filter((p) => p.rate != null || d?.entries.some((e) => e.productId === p.id)), [d]);
  const changed = Object.keys({ ...qty, ...initial }).filter((k) => (qty[k] ?? "") !== (initial[k] ?? ""));
  const locked = !!d?.locked;
  const canEdit = locked ? can("attendance.approve") : can("attendance.record");
  const save = useMutate(() => savePieceWork({ plantId: site, date, reason: locked ? reason.trim() : null,
    entries: changed.map((k) => { const [workerId, productId] = k.split("|"); return { workerId, productId, quantity: Math.round(Number(qty[k] || 0)) }; }) }),
  { success: t("workforce.saved"), invalidate: [[...WF_KEY, "pieceWork"]] });

  const rateOf = (pid: string) => d?.products.find((p) => p.id === pid)?.rate ?? 0;
  const total = Object.entries(qty).reduce((s, [k, v]) => s + Number(v || 0) * rateOf(k.split("|")[1]), 0);

  return (
    <Card>
      <div className="flex flex-wrap items-center gap-2 border-b border-line px-5 py-4">
        <Select value={site} onChange={(e) => setPlantId(e.target.value)} disabled={!!pinned} className="h-10 w-40" aria-label={t("workforce.col.site")}>
          {sites.map((p) => <option key={p.id} value={p.id}>{p.code}</option>)}
        </Select>
        <div className="flex items-center rounded-xl border border-line bg-white px-2">
          <DateInput variant="bare" value={date} max={todayISO()} onChange={(v) => v && setDate(v)} aria-label={t("workforce.col.date")} />
        </div>
        <span className="ml-auto text-sm font-semibold text-navy-800">{t("workforce.piece.dayTotal")}: {formatRWF(total)}</span>
      </div>
      {locked ? <div className="px-5 pt-4"><Alert tone="amber" title={t("workforce.piece.lockedTitle")}><span className="inline-flex items-center gap-1"><Lock className="size-3.5" /> {canEdit ? t("workforce.piece.lockedManager") : t("workforce.piece.lockedText")}</span></Alert></div> : null}
      {q.isError ? (
        <ErrorState error={q.error} retry={() => q.refetch()} />
      ) : q.isLoading || !d ? (
        <div className="flex justify-center py-14"><Spinner /></div>
      ) : !d.workers.length ? (
        <EmptyState title={t("workforce.piece.noWorkers")} description={t("workforce.piece.noWorkersHint")} />
      ) : !products.length ? (
        <EmptyState title={t("workforce.piece.noRates")} description={t("workforce.piece.noRatesHint")} />
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <caption className="sr-only">{t("workforce.tab.pieceWork")}</caption>
            <thead>
              <tr className="border-b border-line bg-slate-50/70 text-[11px] font-semibold tracking-wide whitespace-nowrap text-slate-500 uppercase">
                <th scope="col" className="px-4 py-2.5 text-left">{t("workforce.col.worker")}</th>
                {products.map((p) => (
                  <th key={p.id} scope="col" className="px-3 py-2.5 text-right">
                    {p.code}
                    <span className="block font-normal normal-case">{p.rate != null ? t("workforce.piece.perUnit", { rate: formatInt(p.rate) }) : t("workforce.piece.noRate")}</span>
                  </th>
                ))}
                <th scope="col" className="px-4 py-2.5 text-right">{t("workforce.col.amount")}</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-line">
              {d.workers.map((w) => {
                const amount = products.reduce((s, p) => s + Number(qty[cellKey(w.workerId, p.id)] || 0) * (p.rate ?? 0), 0);
                return (
                  <tr key={w.workerId}>
                    <th scope="row" className="px-4 py-2 text-left font-medium text-slate-900">
                      {w.name}
                      <span className="block text-xs font-normal text-slate-500">{w.jobTitle} · {tx(`workforce.basis.${w.rateBasis}`)}</span>
                    </th>
                    {products.map((p) => {
                      const k = cellKey(w.workerId, p.id);
                      return (
                        <td key={p.id} className="px-3 py-2 text-right">
                          <Input value={qty[k] ?? ""} inputMode="numeric" disabled={!canEdit} onChange={(e) => setQty((m) => ({ ...m, [k]: e.target.value.replace(/[^\d]/g, "") }))}
                            aria-label={t("workforce.piece.qtyFor", { worker: w.name, product: p.code })} className="h-9 w-24 text-right tabular" />
                        </td>
                      );
                    })}
                    <td className="tabular px-4 py-2 text-right font-semibold text-slate-900">{formatInt(amount)}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      {d && d.workers.length && products.length ? (
        <div className="flex flex-wrap items-end justify-between gap-3 border-t border-line px-5 py-4">
          {locked && canEdit ? (
            <Field label={t("workforce.piece.reason")} required className="min-w-64 flex-1" htmlFor="pw-reason">
              <Input id="pw-reason" value={reason} onChange={(e) => setReason(e.target.value)} maxLength={300} />
            </Field>
          ) : <span className="text-xs text-slate-500">{t("workforce.piece.zeroRemoves")}</span>}
          <div className="flex items-center gap-2">
            <Button variant="ghost" size="sm" icon={<Undo2 className="size-3.5" />} disabled={!changed.length || save.isPending} onClick={() => setQty(initial)}>{t("workforce.discard")}</Button>
            <Button icon={<Save className="size-4" />} loading={save.isPending} disabled={!canEdit || !changed.length || (locked && reason.trim().length < 3)} onClick={() => save.mutate(undefined)}>{t("workforce.save")}</Button>
          </div>
        </div>
      ) : null}
    </Card>
  );
}
