import { memo, useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Pencil, RotateCcw, Search } from "lucide-react";
import { toast } from "sonner";
import { Badge, Button, Card, EmptyState, ErrorState, Input, Modal, PageHeader, Select, Spinner, Textarea, Checkbox } from "@/components/ui";
import { CATALOGS, LANGS, LANG_NAMES, setOverrides, useT, type Lang } from "@/i18n";
import { ApiError } from "@/lib/api";
import { listOverrides, revertOverride, saveOverride, type Override } from "./api";

const KEYS = Object.keys(CATALOGS.en).sort();
const NAMESPACES = Array.from(new Set(KEYS.map((k) => k.split(".")[0]!)));
const PAGE = 60;
const OVERRIDES_KEY = ["admin", "i18n"] as const;

type OverrideMap = Record<Lang, Record<string, string>>;

const Row = memo(function Row({ k, ov, onEdit }: { k: string; ov: OverrideMap; onEdit: (k: string) => void }) {
  const { t } = useT();
  return (
    <tr className="align-top">
      <td className="min-w-[15rem] max-w-[20rem] px-4 py-3"><code className="text-[11.5px] break-all text-slate-500">{k}</code></td>
      {LANGS.map((l) => (
        <td key={l} className="px-4 py-3 text-[13px]" lang={l}>
          {ov[l][k] !== undefined ? (
            <>
              <p className="text-slate-900">{ov[l][k]}</p>
              <p className="mt-1 text-[11.5px] text-slate-400 line-through">{CATALOGS[l][k]}</p>
              <Badge tone="amber" className="mt-1">{t("superadmin.content.overridden")}</Badge>
            </>
          ) : <p className="text-slate-700">{CATALOGS[l][k]}</p>}
        </td>
      ))}
      <td className="px-4 py-3 text-right"><Button size="icon" variant="ghost" aria-label={t("common.edit")} title={t("common.edit")} onClick={() => onEdit(k)}><Pencil className="size-4" /></Button></td>
    </tr>
  );
});

export function ContentPage() {
  const { t } = useT();
  const qc = useQueryClient();
  const { data, isLoading, error, refetch } = useQuery({ queryKey: OVERRIDES_KEY, queryFn: ({ signal }) => listOverrides(signal) });
  const [q, setQ] = useState("");
  const [ns, setNs] = useState("");
  const [onlyOverridden, setOnlyOverridden] = useState(false);
  const [limit, setLimit] = useState(PAGE);
  const [editing, setEditing] = useState<string | null>(null);
  const [vals, setVals] = useState<Record<Lang, string>>({ en: "", am: "", rw: "" });
  const [busy, setBusy] = useState(false);

  const ov = useMemo<OverrideMap>(() => {
    const m: OverrideMap = { en: {}, am: {}, rw: {} };
    for (const o of data ?? []) if (m[o.lang]) m[o.lang][o.key] = o.value;
    return m;
  }, [data]);

  const rows = useMemo(() => {
    const needle = q.trim().toLowerCase();
    return KEYS.filter((k) => {
      if (ns && !k.startsWith(`${ns}.`)) return false;
      if (onlyOverridden && !LANGS.some((l) => ov[l][k] !== undefined)) return false;
      if (!needle) return true;
      return k.toLowerCase().includes(needle) || LANGS.some((l) => (CATALOGS[l][k] ?? "").toLowerCase().includes(needle) || (ov[l][k] ?? "").toLowerCase().includes(needle));
    });
  }, [q, ns, onlyOverridden, ov]);

  function open(k: string) {
    setEditing(k);
    setVals({ en: ov.en[k] ?? CATALOGS.en[k] ?? "", am: ov.am[k] ?? CATALOGS.am[k] ?? "", rw: ov.rw[k] ?? CATALOGS.rw[k] ?? "" });
  }

  /** push the refreshed override set into the live i18n store so the UI updates immediately */
  async function sync() {
    const fresh = await qc.fetchQuery({ queryKey: OVERRIDES_KEY, queryFn: () => listOverrides(), staleTime: 0 });
    for (const l of LANGS) setOverrides(l, Object.fromEntries(fresh.filter((o: Override) => o.lang === l).map((o: Override) => [o.key, o.value])));
  }

  async function save() {
    if (!editing) return;
    setBusy(true);
    try {
      for (const l of LANGS) {
        const shipped = CATALOGS[l][editing] ?? "";
        const current = ov[l][editing] ?? shipped;
        if (vals[l] === current) continue;
        if (!vals[l].trim()) return toast.error(t("superadmin.content.empty"));
        if (vals[l] === shipped && ov[l][editing] !== undefined) await revertOverride(l, editing);
        else await saveOverride({ lang: l, key: editing, value: vals[l] });
      }
      await sync();
      toast.success(t("superadmin.content.saved"));
      setEditing(null);
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t("common.somethingWrong"));
    } finally {
      setBusy(false);
    }
  }

  async function revert(l: Lang) {
    if (!editing) return;
    setBusy(true);
    try {
      await revertOverride(l, editing);
      await sync();
      setVals((v) => ({ ...v, [l]: CATALOGS[l][editing] ?? "" }));
      toast.success(t("superadmin.content.reverted"));
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t("common.somethingWrong"));
    } finally {
      setBusy(false);
    }
  }

  const visible = rows.slice(0, limit);
  return (
    <div>
      <PageHeader title={t("superadmin.content.title")} description={t("superadmin.content.description")} />
      <Card className="mb-4 flex flex-wrap items-center gap-3 p-4">
        <div className="relative min-w-56 flex-1">
          <Search className="pointer-events-none absolute top-1/2 left-3.5 size-4 -translate-y-1/2 text-slate-400" />
          <Input type="search" value={q} onChange={(e) => { setQ(e.target.value); setLimit(PAGE); }} placeholder={t("superadmin.content.search")} aria-label={t("superadmin.content.search")} className="pl-10" />
        </div>
        <Select value={ns} onChange={(e) => { setNs(e.target.value); setLimit(PAGE); }} className="w-48" aria-label={t("superadmin.content.area")}>
          <option value="">{t("superadmin.content.allAreas")}</option>
          {NAMESPACES.map((n) => <option key={n} value={n}>{n}</option>)}
        </Select>
        <Checkbox checked={onlyOverridden} onChange={(e) => { setOnlyOverridden(e.target.checked); setLimit(PAGE); }} label={t("superadmin.content.onlyOverridden")} />
        <span className="text-xs text-slate-500">{t("superadmin.content.count", { count: rows.length })}</span>
      </Card>

      <Card className="overflow-hidden">
        {isLoading ? <div className="grid place-items-center py-14"><Spinner /></div> : error ? <ErrorState error={error} retry={() => void refetch()} /> : !rows.length ? <EmptyState icon={<Search />} title={t("superadmin.content.none")} /> : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[56rem] text-sm">
              <thead>
                <tr className="border-b border-line bg-slate-50/70 text-left text-[11px] font-semibold tracking-wide text-slate-500 uppercase">
                  <th className="px-4 py-2.5">{t("superadmin.content.key")}</th>
                  {LANGS.map((l) => <th key={l} className="px-4 py-2.5">{LANG_NAMES[l]}</th>)}
                  <th className="px-4 py-2.5"><span className="sr-only">{t("common.actions")}</span></th>
                </tr>
              </thead>
              <tbody className="divide-y divide-line">
                {visible.map((k) => <Row key={k} k={k} ov={ov} onEdit={open} />)}
              </tbody>
            </table>
            {rows.length > limit ? <div className="border-t border-line p-3 text-center"><Button variant="secondary" size="sm" onClick={() => setLimit((n) => n + PAGE)}>{t("superadmin.content.more", { count: rows.length - limit })}</Button></div> : null}
          </div>
        )}
      </Card>

      <Modal open={!!editing} onClose={() => !busy && setEditing(null)} size="lg" title={t("superadmin.content.editTitle")} description={editing ?? undefined}
        footer={<><Button variant="secondary" onClick={() => setEditing(null)} disabled={busy}>{t("common.cancel")}</Button><Button loading={busy} onClick={() => void save()}>{t("common.save")}</Button></>}>
        <p className="mb-4 rounded-lg bg-amber-50 px-3 py-2 text-xs text-amber-900">{t("superadmin.content.placeholderNote")}</p>
        <div className="space-y-5">
          {editing ? LANGS.map((l) => (
            <div key={l}>
              <div className="mb-1 flex items-center justify-between gap-2">
                <label htmlFor={`ov-${l}`} className="text-[13px] font-semibold text-slate-700">{LANG_NAMES[l]}</label>
                {ov[l][editing] !== undefined ? <Button size="sm" variant="ghost" icon={<RotateCcw className="size-3.5" />} disabled={busy} onClick={() => void revert(l)}>{t("superadmin.content.revert")}</Button> : null}
              </div>
              <Textarea id={`ov-${l}`} lang={l} rows={2} value={vals[l]} onChange={(e) => setVals((v) => ({ ...v, [l]: e.target.value }))} />
              <p className="mt-1 text-[11.5px] text-slate-400">{t("superadmin.content.shipped")}: <span lang={l}>{CATALOGS[l][editing]}</span></p>
            </div>
          )) : null}
        </div>
      </Modal>
    </div>
  );
}
