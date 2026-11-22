import { Fragment, useEffect, useMemo, useState } from "react";
import { useSearchParams } from "react-router";
import { ChevronRight, ScrollText, X } from "lucide-react";
import { Badge, Card, EmptyState, ErrorState, FilterBar, PageHeader, Pagination, Select, Spinner, useFilters, Checkbox } from "@/components/ui";
import { useT, type T } from "@/i18n";
import { formatDateTime } from "@/lib/dates";
import { cn } from "@/lib/cn";
import { useAudit, type AuditEntry } from "./api";
import { SearchInput, useDebounced } from "./components";

const ENTITIES: string[] = ["User", "Setting", "Plant", "Product", "Material", "Customer", "ProductionReport", "StockMovement", "MaterialMovement", "MaterialTransfer", "Sale", "Delivery", "Payment", "PettyCashEntry", "Worker", "PayrollPeriod", "Import"];

/** Colour by verb so approvals and destructive actions stand out in the list. */
function actionTone(action: string) {
  const verb = action.split(".").at(-1) ?? "";
  if (/(approve|authorize|confirm|pay|verify|check|activate|unlock)/.test(verb)) return "green" as const;
  if (/(reject|cancel|delete|deactivate|revoke|replace)/.test(verb)) return "red" as const;
  if (/(create|import|excel|submit|prepare)/.test(verb)) return "blue" as const;
  if (/(reset|password)/.test(verb)) return "amber" as const;
  return "slate" as const;
}

export function AuditPage() {
  const { t, tx } = useT();
  const { filters, set } = useFilters();
  const [params, setParams] = useSearchParams();
  const entity = params.get("entity") ?? "";
  const entityId = params.get("entityId") ?? "";
  const page = Math.max(1, Number(params.get("page")) || 1);
  const [search, setSearch] = useState(params.get("q") ?? "");
  const q = useDebounced(search.trim());
  useEffect(() => {
    if (q !== (params.get("q") ?? "")) set({ q: q || undefined });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [q]);

  const audit = useAudit({ from: filters.from, to: filters.to, entity, entityId, q: params.get("q") ?? "", page });
  const [open, setOpen] = useState<Set<string>>(new Set());
  const toggle = (id: string) => setOpen((s) => {
    const n = new Set(s);
    if (n.has(id)) n.delete(id);
    else n.add(id);
    return n;
  });
  const setPage = (p: number) => {
    const n = new URLSearchParams(params);
    n.set("page", String(p));
    setParams(n, { replace: true });
    window.scrollTo({ top: 0, behavior: "smooth" });
  };

  return (
    <>
      <PageHeader
        title={t("admin.audit.title")}
        description={t("admin.audit.description")}
        breadcrumbs={[{ label: t("admin.breadcrumb") }, { label: t("admin.audit.title") }]}
      />
      <Card>
        <div className="space-y-3 px-5 py-4">
          <FilterBar filters={filters} onChange={set} showPlant={false}>
            <Select value={entity} onChange={(e) => set({ entity: e.target.value || undefined, entityId: undefined })} className="h-10 w-48" aria-label={t("admin.audit.recordType")}>
              <option value="">{t("admin.audit.allTypes")}</option>
              {ENTITIES.map((e) => <option key={e} value={e}>{tx(`admin.audit.entityPlural.${e}`)}</option>)}
            </Select>
            <SearchInput value={search} onChange={setSearch} placeholder={t("admin.audit.search")} className="w-full sm:w-64" />
          </FilterBar>
          {entityId ? (
            <div className="flex flex-wrap items-center gap-2 text-sm text-slate-600">
              {t("admin.audit.showingOne")}
              <span className="inline-flex items-center gap-1 rounded-lg bg-navy-50 py-1 pr-1 pl-2.5 font-mono text-xs text-navy-800">
                {entity ? `${entity} · ` : ""}{entityId}
                <button onClick={() => set({ entityId: undefined })} className="rounded p-0.5 hover:bg-navy-100" aria-label={t("admin.audit.clearRecord")}><X className="size-3.5" /></button>
              </span>
            </div>
          ) : null}
        </div>

        {audit.isError ? (
          <ErrorState error={audit.error} retry={() => audit.refetch()} />
        ) : audit.isLoading ? (
          <div className="border-t border-line py-14"><Spinner className="mx-auto" /></div>
        ) : !audit.data?.items.length ? (
          <div className="border-t border-line">
            <EmptyState icon={<ScrollText />} title={t("admin.audit.emptyTitle")} description={t("admin.audit.emptyText")} />
          </div>
        ) : (
          <>
            <div className={cn("overflow-x-auto transition-opacity", audit.isFetching && "opacity-60")}>
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-y border-line bg-slate-50/70 text-left text-[11px] font-semibold tracking-wide whitespace-nowrap text-slate-500 uppercase">
                    <th className="w-8 py-2.5 pl-4" aria-label={t("admin.audit.expand")} />
                    <th className="px-3 py-2.5">{t("admin.audit.col.time")}</th>
                    <th className="px-3 py-2.5">{t("admin.audit.col.user")}</th>
                    <th className="px-3 py-2.5">{t("admin.audit.col.action")}</th>
                    <th className="hidden px-3 py-2.5 md:table-cell">{t("admin.audit.col.record")}</th>
                    <th className="hidden px-3 py-2.5 lg:table-cell">{t("admin.audit.col.summary")}</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-line">
                  {audit.data.items.map((a) => {
                    const expanded = open.has(a.id);
                    return (
                      <Fragment key={a.id}>
                        <tr onClick={() => toggle(a.id)} className={cn("cursor-pointer align-top transition-colors hover:bg-navy-50/40", expanded && "bg-navy-50/30")}>
                          <td className="py-3 pl-4">
                            <button className="grid size-6 place-items-center rounded-md text-slate-400 hover:bg-slate-100 hover:text-slate-700" aria-expanded={expanded} aria-label={expanded ? t("admin.audit.hide") : t("admin.audit.show")} onClick={(e) => { e.stopPropagation(); toggle(a.id); }}>
                              <ChevronRight className={cn("size-4 transition-transform", expanded && "rotate-90")} />
                            </button>
                          </td>
                          <td className="px-3 py-3 whitespace-nowrap text-slate-600 tabular">{formatDateTime(a.createdAt)}</td>
                          <td className="px-3 py-3">
                            {a.user ? (
                              <div className="min-w-0">
                                <p className="truncate font-medium text-slate-800">{a.user.name}</p>
                                <p className="hidden truncate text-xs text-slate-500 sm:block">{a.user.email}</p>
                              </div>
                            ) : <span className="text-slate-400">{t("admin.audit.system")}</span>}
                          </td>
                          <td className="px-3 py-3"><Badge tone={actionTone(a.action)} className="text-[11px]"><span title={a.action}>{tx(`admin.audit.action.${a.action}`, undefined, a.action)}</span></Badge></td>
                          <td className="hidden px-3 py-3 md:table-cell">
                            <p className="font-medium text-slate-700">{tx(`admin.audit.entity.${a.entity}`, undefined, a.entity)}</p>
                            {a.entityId ? (
                              <button
                                onClick={(e) => { e.stopPropagation(); set({ entity: a.entity, entityId: a.entityId ?? undefined }); }}
                                className="font-mono text-[11px] text-slate-400 hover:text-navy-700 hover:underline"
                                title={t("admin.audit.showAll")}
                              >
                                {a.entityId.length > 14 ? `${a.entityId.slice(0, 12)}…` : a.entityId}
                              </button>
                            ) : null}
                          </td>
                          <td className="hidden max-w-md px-3 py-3 text-slate-600 lg:table-cell"><p className="line-clamp-2">{a.summary ?? <span className="text-slate-400">—</span>}</p></td>
                        </tr>
                        {expanded ? (
                          <tr className="bg-slate-50/60">
                            <td colSpan={6} className="px-4 pt-1 pb-4 sm:pl-12">
                              <AuditDetail entry={a} />
                            </td>
                          </tr>
                        ) : null}
                      </Fragment>
                    );
                  })}
                </tbody>
              </table>
            </div>
            <Pagination page={audit.data.page} pageSize={audit.data.pageSize} total={audit.data.total} onPage={setPage} />
          </>
        )}
      </Card>
    </>
  );
}

// ───────────────────────────── diff ─────────────────────────────

type Flat = Record<string, unknown>;

/** Flatten nested objects to dotted keys ("plant.code"); arrays stay whole. */
export function flatten(value: unknown, prefix = "", out: Flat = {}): Flat {
  if (value && typeof value === "object" && !Array.isArray(value)) {
    const entries = Object.entries(value as Record<string, unknown>);
    if (!entries.length && prefix) out[prefix] = {};
    for (const [k, v] of entries) flatten(v, prefix ? `${prefix}.${k}` : k, out);
  } else if (prefix) out[prefix] = value;
  else out["value"] = value;
  return out;
}

const HIDDEN = new Set(["passwordHash", "tokenHash", "codeHash"]);

function show(v: unknown, t: T): string {
  if (v === undefined) return "";
  if (v === null) return "—";
  if (typeof v === "boolean") return v ? t("common.yes") : t("common.no");
  if (typeof v === "string") {
    if (/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}/.test(v)) return v.endsWith("T00:00:00.000Z") ? v.slice(0, 10) : formatDateTime(v);
    return v === "" ? t("admin.audit.empty") : v;
  }
  if (typeof v === "number") return v.toLocaleString("en-US");
  return JSON.stringify(v);
}

function AuditDetail({ entry }: { entry: AuditEntry }) {
  const { t } = useT();
  const [all, setAll] = useState(false);
  const rows = useMemo(() => {
    const before = entry.before == null ? null : flatten(entry.before);
    const after = entry.after == null ? null : flatten(entry.after);
    const keys = [...new Set([...Object.keys(before ?? {}), ...Object.keys(after ?? {})])].filter((k) => !HIDDEN.has(k.split(".").at(-1)!));
    return keys.map((key) => {
      const b = before?.[key];
      const a = after?.[key];
      const changed = !!before && !!after && JSON.stringify(b) !== JSON.stringify(a);
      return { key, before: b, after: a, changed };
    });
  }, [entry]);

  const mode = entry.before != null && entry.after != null ? "update" : entry.after != null ? "create" : entry.before != null ? "delete" : "none";
  const changedCount = rows.filter((r) => r.changed).length;
  const visible = mode === "update" && !all ? rows.filter((r) => r.changed) : rows;

  return (
    <div className="space-y-3 pt-2">
      <dl className="flex flex-wrap gap-x-6 gap-y-1 text-xs text-slate-500">
        <div><dt className="inline font-medium text-slate-600">{t("admin.audit.when")} </dt><dd className="inline">{formatDateTime(entry.createdAt)}</dd></div>
        {entry.user ? <div><dt className="inline font-medium text-slate-600">{t("admin.audit.by")} </dt><dd className="inline">{entry.user.name} ({entry.user.email})</dd></div> : null}
        {entry.entityId ? <div><dt className="inline font-medium text-slate-600">{t("admin.audit.recordId")} </dt><dd className="inline font-mono">{entry.entityId}</dd></div> : null}
        {entry.ip ? <div><dt className="inline font-medium text-slate-600">{t("admin.audit.ip")} </dt><dd className="inline font-mono">{entry.ip}</dd></div> : null}
      </dl>
      {entry.summary ? <p className="text-sm text-slate-700">{entry.summary}</p> : null}

      {mode === "none" ? (
        <p className="text-sm text-slate-500">{t("admin.audit.noValues")}</p>
      ) : (
        <div className="overflow-hidden rounded-xl border border-line bg-white">
          <div className="flex flex-wrap items-center justify-between gap-2 border-b border-line px-4 py-2.5">
            <p className="text-xs font-semibold text-slate-600">
              {mode === "update" ? t("admin.audit.fieldsChanged", { count: changedCount }) : mode === "create" ? t("admin.audit.valuesRecorded") : t("admin.audit.valuesBefore")}
            </p>
            {mode === "update" && rows.length > changedCount ? <Checkbox label={t("admin.audit.showUnchanged")} checked={all} onChange={(e) => setAll(e.target.checked)} className="text-xs" /> : null}
          </div>
          {visible.length ? (
            <div className="max-h-96 overflow-auto">
              <table className="w-full text-[13px]">
                <thead className="sticky top-0 bg-white">
                  <tr className="text-left text-[11px] font-semibold tracking-wide text-slate-500 uppercase">
                    <th className="w-1/4 px-4 py-2">{t("admin.audit.field")}</th>
                    {mode !== "create" ? <th className="px-4 py-2">{mode === "update" ? t("admin.audit.before") : t("admin.audit.value")}</th> : null}
                    {mode !== "delete" ? <th className="px-4 py-2">{mode === "update" ? t("admin.audit.after") : t("admin.audit.value")}</th> : null}
                  </tr>
                </thead>
                <tbody className="divide-y divide-line">
                  {visible.map((r) => (
                    <tr key={r.key} className={cn(r.changed && "bg-amber-50/70")}>
                      <td className={cn("px-4 py-1.5 align-top font-mono text-xs", r.changed ? "font-semibold text-amber-900" : "text-slate-500")}>{r.key}</td>
                      {mode !== "create" ? <td className={cn("px-4 py-1.5 align-top break-all", r.changed ? "text-red-700 line-through decoration-red-300" : "text-slate-700")}>{show(r.before, t)}</td> : null}
                      {mode !== "delete" ? <td className={cn("px-4 py-1.5 align-top break-all", r.changed ? "font-medium text-emerald-800" : "text-slate-700")}>{show(r.after, t)}</td> : null}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <p className="px-4 py-3 text-sm text-slate-500">{t("admin.audit.identical")}</p>
          )}
        </div>
      )}
    </div>
  );
}
