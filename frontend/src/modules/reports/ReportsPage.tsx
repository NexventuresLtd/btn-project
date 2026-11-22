import { useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { BarChart3, Boxes, Download, ExternalLink, Factory, FileText, ShoppingCart, Wallet } from "lucide-react";
import { useAuth } from "@/auth/AuthProvider";
import { useLookups } from "@/hooks/useLookups";
import { Button, Card, EmptyState, ErrorState, Field, Input, PageHeader, PageLoader, Select, ShapeToggle, usePdfShape } from "@/components/ui";
import { downloadPdf, get, openPdf, qs, type PdfShape } from "@/lib/api";
import { cn } from "@/lib/cn";
import { monthStartISO, todayISO } from "@/lib/dates";
import { useT } from "@/i18n";

type ReportParam = "range" | "date" | "plant" | "plantRequired";
type ReportGroup = "Production" | "Inventory" | "Sales" | "Finance" | "Management";

interface ReportEntry {
  key: string;
  title: string;
  description: string;
  group: ReportGroup;
  path: string;
  params: ReportParam[];
  permission: string;
}

const GROUP_META: Record<ReportGroup, { icon: ReactNode; tone: string }> = {
  Production: { icon: <Factory />, tone: "bg-navy-50 text-navy-700" },
  Inventory: { icon: <Boxes />, tone: "bg-teal-50 text-teal-700" },
  Sales: { icon: <ShoppingCart />, tone: "bg-amber-50 text-amber-700" },
  Finance: { icon: <Wallet />, tone: "bg-blue-50 text-blue-700" },
  Management: { icon: <BarChart3 />, tone: "bg-purple-50 text-purple-700" },
};

function useCatalog() {
  return useQuery({
    queryKey: ["reports", "catalog"],
    queryFn: ({ signal }) => get<{ groups: { group: ReportGroup; reports: ReportEntry[] }[] }>("/reports/catalog", signal),
    staleTime: 10 * 60_000,
  });
}

function ReportCard({ report }: { report: ReportEntry }) {
  const { t, tx } = useT();
  const { user } = useAuth();
  const { data: lookups } = useLookups();
  const pinned = user?.plant && ["SUPERVISOR", "LEAD_OPERATOR", "OPERATOR"].includes(user.role) ? user.plant.id : "";
  const [from, setFrom] = useState(monthStartISO());
  const [to, setTo] = useState(todayISO());
  const [date, setDate] = useState(todayISO());
  const [plantId, setPlantId] = useState(pinned);
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

  const url = () =>
    `${report.path}${qs({
      ...(has("range") ? { from, to } : {}),
      ...(has("date") ? { date } : {}),
      ...(showPlant ? { plantId: plantId || undefined } : {}),
    })}`;

  const go = (download: boolean) => {
    setTouched(true);
    if (!valid) return;
    if (download) downloadPdf(url(), shape);
    else openPdf(url(), shape);
  };

  const meta = GROUP_META[report.group];
  const id = `r-${report.key}`;
  return (
    <Card className="flex flex-col p-5 transition hover:shadow-[0_2px_4px_rgb(12_35_75/0.05),0_12px_28px_rgb(12_35_75/0.08)]">
      <div className="flex items-start gap-3">
        <span className={cn("grid size-10 shrink-0 place-items-center rounded-xl [&>svg]:size-5", meta.tone)}><FileText /></span>
        <div className="min-w-0">
          <h3 className="text-[15px] font-semibold text-slate-900">{tx(`reports.item.${report.key}.title`, undefined, report.title)}</h3>
          <p className="mt-1 text-[13px] leading-relaxed text-slate-500">{tx(`reports.item.${report.key}.desc`, undefined, report.description)}</p>
        </div>
      </div>

      <div className="mt-5 grid flex-1 content-start gap-3 sm:grid-cols-2">
        {has("range") ? (
          <>
            <Field label={t("common.from")} htmlFor={`${id}-from`}>
              <Input id={`${id}-from`} type="date" value={from} max={to} onChange={(e) => e.target.value && setFrom(e.target.value)} />
            </Field>
            <Field label={t("common.to")} htmlFor={`${id}-to`} error={touched ? errors.to : undefined}>
              <Input id={`${id}-to`} type="date" value={to} min={from} max={todayISO()} onChange={(e) => e.target.value && setTo(e.target.value)} invalid={touched && !!errors.to} />
            </Field>
          </>
        ) : null}
        {has("date") ? (
          <Field label={t("reports.asAt")} htmlFor={`${id}-date`}>
            <Input id={`${id}-date`} type="date" value={date} max={todayISO()} onChange={(e) => e.target.value && setDate(e.target.value)} />
          </Field>
        ) : null}
        {showPlant ? (
          <Field label={t("common.plant")} htmlFor={`${id}-plant`} required={plantRequired} error={touched ? errors.plantId : undefined} className={has("date") ? "" : has("range") ? "sm:col-span-2" : ""}>
            <Select id={`${id}-plant`} value={plantId} onChange={(e) => setPlantId(e.target.value)} disabled={!!pinned} invalid={touched && !!errors.plantId}>
              {plantRequired ? <option value="" disabled>{t("reports.selectPlant")}</option> : <option value="">{t("common.allPlants")}</option>}
              {lookups?.plants.map((p) => <option key={p.id} value={p.id}>{p.code} — {p.name}</option>)}
            </Select>
          </Field>
        ) : null}
        {!report.params.length ? <p className="text-[13px] text-slate-500 sm:col-span-2">{t("reports.noOptions")}</p> : null}
        <Field label={t("common.pageShape")} hint={t("common.shapeHint")} className="sm:col-span-2">
          <ShapeToggle value={shape} onChange={setShape} />
        </Field>
      </div>

      <div className="mt-5 flex flex-wrap gap-2 border-t border-line pt-4">
        <Button onClick={() => go(false)} icon={<ExternalLink className="size-4" />} className="flex-1 sm:flex-none">{t("reports.open")}</Button>
        <Button variant="secondary" onClick={() => go(true)} icon={<Download className="size-4" />} className="flex-1 sm:flex-none">{t("reports.download")}</Button>
      </div>
    </Card>
  );
}

export function ReportsPage() {
  const { t } = useT();
  const { data, isLoading, error, refetch } = useCatalog();
  return (
    <div>
      <PageHeader title={t("reports.title")} description={t("reports.subtitle")} />
      {isLoading ? (
        <PageLoader />
      ) : error || !data ? (
        <Card><ErrorState error={error} retry={() => void refetch()} /></Card>
      ) : data.groups.length === 0 ? (
        <Card><EmptyState icon={<FileText />} title={t("reports.empty.title")} description={t("reports.empty.text")} /></Card>
      ) : (
        <div className="space-y-10">
          {data.groups.map((g) => {
            const meta = GROUP_META[g.group];
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
                  {g.reports.map((r) => <ReportCard key={r.key} report={r} />)}
                </div>
              </section>
            );
          })}
        </div>
      )}
    </div>
  );
}
