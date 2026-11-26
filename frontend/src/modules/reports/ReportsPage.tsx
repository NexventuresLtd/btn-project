import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router";
import { BarChart3, Boxes, CalendarClock, Download, ExternalLink, Factory, FileText, Search, ShieldCheck, ShoppingCart, Store, Users, Wallet } from "lucide-react";
import { useAuth } from "@/auth/AuthProvider";
import { useLookups } from "@/hooks/useLookups";
import { Button, Card, EmptyState, ErrorState, ExportMenu, Field, Input, PageHeader, PageLoader, Select, ShapeToggle, usePdfShape, DateInput } from "@/components/ui";
import { DataCutoff } from "@/components/DataCutoff";
import { downloadPdf, get, openPdf, qs, type PdfShape } from "@/lib/api";
import { cn } from "@/lib/cn";
import { monthStartISO, todayISO } from "@/lib/dates";
import { useT } from "@/i18n";
import { pinnedPlantId } from "@/lib/scope";
import { useReportCache } from "@/offline/cache";

type ReportParam = "range" | "date" | "plant" | "plantRequired";
type ReportGroup = "Production" | "Inventory" | "Sales" | "Procurement" | "Finance" | "Workforce" | "Management" | "Administration";

interface ReportFilter { key: string; label: string; options: { value: string; label: string }[] }
export interface ReportEntry {
  key: string;
  title: string;
  description: string;
  group: ReportGroup;
  path: string;
  exportPath: string;
  params: ReportParam[];
  filters: ReportFilter[];
  formats: string[];
  permission: string;
  permissions: string[];
}

const GROUP_META: Record<ReportGroup, { icon: ReactNode; tone: string }> = {
  Production: { icon: <Factory />, tone: "bg-navy-50 text-navy-700" },
  Inventory: { icon: <Boxes />, tone: "bg-teal-50 text-teal-700" },
  Sales: { icon: <ShoppingCart />, tone: "bg-amber-50 text-amber-700" },
  Procurement: { icon: <Store />, tone: "bg-orange-50 text-orange-700" },
  Finance: { icon: <Wallet />, tone: "bg-blue-50 text-blue-700" },
  Workforce: { icon: <Users />, tone: "bg-emerald-50 text-emerald-700" },
  Management: { icon: <BarChart3 />, tone: "bg-purple-50 text-purple-700" },
  Administration: { icon: <ShieldCheck />, tone: "bg-slate-100 text-slate-700" },
};

function useCatalog() {
  const cached = useReportCache();
  return useQuery({
    queryKey: ["reports", "catalog"],
    networkMode: "always",
    queryFn: cached("reports:catalog", "", (signal) => get<{ groups: { group: ReportGroup; reports: ReportEntry[] }[] }>("/reports/catalog", signal)),
    staleTime: 10 * 60_000,
  });
}

function ReportCard({ report, highlight }: { report: ReportEntry; highlight: boolean }) {
  const { t, tx } = useT();
  const { user } = useAuth();
  const { data: lookups } = useLookups();
  const pinned = pinnedPlantId(user) ?? "";
  const [from, setFrom] = useState(monthStartISO());
  const [to, setTo] = useState(todayISO());
  const [date, setDate] = useState(todayISO());
  const [plantId, setPlantId] = useState(pinned);
  const [dims, setDims] = useState<Record<string, string>>({});
  const [touched, setTouched] = useState(false);
  const [preferredShape, setPreferredShape] = usePdfShape();
  const [shape, setShapeState] = useState<PdfShape>(preferredShape);
  const setShape = (s: PdfShape) => {
    setShapeState(s);
    setPreferredShape(s);
  };

  const has = (p: ReportParam) => report.params.includes(p);
  const plantRequired = has("plantRequired");
  const showPlant = plantRequired || has("plant");
  const errors: Record<string, string | undefined> = {
    plantId: plantRequired && !plantId ? t("reports.err.plant") : undefined,
    to: has("range") && from > to ? t("reports.err.range") : undefined,
  };
  const valid = !errors.plantId && !errors.to;

  const params: Record<string, string | undefined> = {
    ...(has("range") ? { from, to } : {}),
    ...(has("date") ? { date } : {}),
    ...(showPlant ? { plantId: plantId || undefined } : {}),
    ...Object.fromEntries(Object.entries(dims).map(([k, v]) => [k, v || undefined])),
  };
  const url = () => `${report.path}${qs(params)}`;

  const go = (download: boolean) => {
    setTouched(true);
    if (!valid) return;
    if (download) downloadPdf(url(), shape);
    else openPdf(url(), shape);
  };

  const meta = GROUP_META[report.group] ?? GROUP_META.Management;
  const id = `r-${report.key}`;
  return (
    <Card id={id} className={cn("flex flex-col p-5 transition hover:shadow-[0_2px_4px_rgb(12_35_75/0.05),0_12px_28px_rgb(12_35_75/0.08)]", highlight && "ring-2 ring-amber-400")}>
      <div className="flex items-start gap-3">
        <span className={cn("grid size-10 shrink-0 place-items-center rounded-xl [&>svg]:size-5", meta.tone)}><FileText /></span>
        <div className="min-w-0">
          <h3 className="text-[15px] font-semibold text-slate-900">{tx(`reports.item.${report.key}.title`, undefined, report.title)}</h3>
          <p className="mt-1 text-[13px] leading-relaxed text-slate-500">{tx(`reports.item.${report.key}.desc`, undefined, report.description)}</p>
          <p className="mt-1.5 text-[11px] font-semibold tracking-wide text-slate-600 uppercase">{t("reports.formats")}: PDF · CSV · Excel · JSON</p>
        </div>
      </div>

      <div className="mt-5 grid flex-1 content-start gap-3 sm:grid-cols-2">
        {has("range") ? (
          <>
            <Field label={t("common.from")} htmlFor={`${id}-from`}>
              <DateInput id={`${id}-from`} value={from} max={to} onChange={(v) => v && setFrom(v)} />
            </Field>
            <Field label={t("common.to")} htmlFor={`${id}-to`} error={touched ? errors.to : undefined}>
              <DateInput id={`${id}-to`} value={to} min={from} max={todayISO()} onChange={(v) => v && setTo(v)} invalid={touched && !!errors.to} />
            </Field>
          </>
        ) : null}
        {has("date") ? (
          <Field label={t("reports.asAt")} htmlFor={`${id}-date`}>
            <DateInput id={`${id}-date`} value={date} max={todayISO()} onChange={(v) => v && setDate(v)} />
          </Field>
        ) : null}
        {showPlant ? (
          <Field label={t("common.plant")} htmlFor={`${id}-plant`} required={plantRequired} error={touched ? errors.plantId : undefined}>
            <Select id={`${id}-plant`} value={plantId} onChange={(e) => setPlantId(e.target.value)} disabled={!!pinned} invalid={touched && !!errors.plantId}>
              {plantRequired ? <option value="" disabled>{t("reports.selectPlant")}</option> : <option value="">{t("common.allPlants")}</option>}
              {lookups?.plants.map((p) => <option key={p.id} value={p.id}>{p.code} — {p.name}</option>)}
            </Select>
          </Field>
        ) : null}
        {report.filters.map((f) => (
          <Field key={f.key} label={f.label} htmlFor={`${id}-${f.key}`}>
            <Select id={`${id}-${f.key}`} value={dims[f.key] ?? ""} onChange={(e) => setDims((d) => ({ ...d, [f.key]: e.target.value }))}>
              <option value="">{t("reports.filterAll")}</option>
              {f.options.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
            </Select>
          </Field>
        ))}
        {!report.params.length && !report.filters.length ? <p className="text-[13px] text-slate-500 sm:col-span-2">{t("reports.noOptions")}</p> : null}
        <Field label={t("common.pageShape")} hint={t("common.shapeHint")} className="sm:col-span-2">
          <ShapeToggle value={shape} onChange={setShape} />
        </Field>
      </div>

      <div className="mt-5 flex flex-wrap gap-2 border-t border-line pt-4">
        <Button onClick={() => go(false)} icon={<ExternalLink className="size-4" />} className="flex-1 sm:flex-none" onlineOnly="download">{t("reports.open")}</Button>
        <Button variant="secondary" onClick={() => go(true)} icon={<Download className="size-4" />} className="flex-1 sm:flex-none" onlineOnly="download">{t("reports.download")}</Button>
        {report.exportPath ? <ExportMenu path={report.exportPath} params={params} className="flex-1 sm:flex-none" /> : null}
      </div>
    </Card>
  );
}

export function ReportsPage() {
  const { t } = useT();
  const { can, user } = useAuth();
  const { data, isLoading, error, refetch } = useCatalog();
  const [params] = useSearchParams();
  const focus = params.get("report");
  const [q, setQ] = useState("");
  const groups = useMemo(() => {
    const needle = q.trim().toLowerCase();
    if (!data) return [];
    return data.groups
      .map((g) => ({ ...g, reports: g.reports.filter((r) => !needle || `${r.title} ${r.description}`.toLowerCase().includes(needle)) }))
      .filter((g) => g.reports.length);
  }, [data, q]);
  useEffect(() => {
    if (focus && data) document.getElementById(`r-${focus}`)?.scrollIntoView({ block: "center" });
  }, [focus, data]);
  const total = data?.groups.reduce((n, g) => n + g.reports.length, 0) ?? 0;

  return (
    <div>
      <PageHeader
        title={t("reports.title")}
        description={t("reports.subtitle")}
        actions={can("reports.schedule") ? (
          <Link to="/reports/schedules" className="inline-flex h-10 items-center gap-2 rounded-xl border border-line bg-white px-4 text-sm font-semibold text-slate-700 hover:bg-slate-50 focus-visible:ring-2 focus-visible:ring-navy-300 focus-visible:outline-none">
            <CalendarClock className="size-4" aria-hidden /> {t("reports.schedules.link")}
          </Link>
        ) : null}
      />
      <div className="mb-6 flex flex-wrap items-center justify-between gap-3">
        <div className="relative w-full max-w-sm">
          <Search className="pointer-events-none absolute top-1/2 left-3.5 size-4 -translate-y-1/2 text-slate-400" aria-hidden />
          <Input type="search" value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("reports.search", { count: total })} aria-label={t("reports.search", { count: total })} className="h-10 pl-10" />
        </div>
        <DataCutoff plantId={pinnedPlantId(user) ?? undefined} />
      </div>
      {isLoading ? (
        <PageLoader />
      ) : error || !data ? (
        <Card><ErrorState error={error} retry={() => void refetch()} /></Card>
      ) : data.groups.length === 0 ? (
        <Card><EmptyState icon={<FileText />} title={t("reports.empty.title")} description={t("reports.empty.text")} /></Card>
      ) : !groups.length ? (
        <Card><EmptyState icon={<Search />} title={t("reports.noMatch")} /></Card>
      ) : (
        <div className="space-y-10">
          {groups.map((g) => {
            const meta = GROUP_META[g.group] ?? GROUP_META.Management;
            return (
              <section key={g.group} aria-labelledby={`grp-${g.group}`}>
                <div className="mb-4 flex items-center gap-3">
                  <span className={cn("grid size-8 place-items-center rounded-lg [&>svg]:size-4", meta.tone)}>{meta.icon}</span>
                  <div>
                    <h2 id={`grp-${g.group}`} className="text-base font-semibold text-slate-900">{t(`reports.group.${g.group}`)}</h2>
                    <p className="text-xs text-slate-500">{t(`reports.group.${g.group}.blurb`)}</p>
                  </div>
                  <span className="ml-auto rounded-md bg-slate-100 px-2 py-0.5 text-xs font-semibold text-slate-500">{g.reports.length}</span>
                </div>
                <div className="grid gap-5 md:grid-cols-2 2xl:grid-cols-3">
                  {g.reports.map((r) => <ReportCard key={r.key} report={r} highlight={focus === r.key} />)}
                </div>
              </section>
            );
          })}
        </div>
      )}
    </div>
  );
}
