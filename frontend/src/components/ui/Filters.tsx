import { useEffect, useRef, useState } from "react";
import { useT } from "@/i18n";
import { useSearchParams } from "react-router";
import { ChevronDown, Download, ExternalLink, FileDown } from "lucide-react";
import { sitesOnly, useLookups } from "@/hooks/useLookups";
import { useAuth } from "@/auth/AuthProvider";
import { monthStartISO, shiftISO, todayISO } from "@/lib/dates";
import { downloadPdf, openPdf, qs } from "@/lib/api";
import { cn } from "@/lib/cn";
import { Button } from "./Button";
import { DateInput } from "./DateInput";
import { Select } from "./Field";
import { ShapeToggle, usePdfShape } from "./PdfShape";
import { pinnedPlantId } from "@/lib/scope";
import { DataCutoff } from "@/components/DataCutoff"; // P7 RPT-06: data cut-off on every report screen

export interface Filters {
  from: string;
  to: string;
  plantId: string;
}

/**
 * Date range + plant filter kept in the URL (?from=&to=&plantId=), so filtered
 * views can be bookmarked and shared. Defaults: month to date, all plants.
 * Plant-scoped users are pinned to their own plant.
 */
export function useFilters(defaults?: Partial<Filters>) {
  const [params, setParams] = useSearchParams();
  const { user } = useAuth();
  const pinnedPlant = pinnedPlantId(user);
  const filters: Filters = {
    from: params.get("from") ?? defaults?.from ?? monthStartISO(),
    to: params.get("to") ?? defaults?.to ?? todayISO(),
    plantId: pinnedPlant ?? params.get("plantId") ?? defaults?.plantId ?? "",
  };
  const set = (patch: Partial<Filters> & Record<string, string | undefined>) => {
    const next = new URLSearchParams(params);
    for (const [k, v] of Object.entries(patch)) { if (v) next.set(k, v); else next.delete(k); }
    next.delete("page");
    setParams(next, { replace: true });
  };
  return { filters, set, pinnedPlant, query: qs({ from: filters.from, to: filters.to, plantId: filters.plantId }) };
}

export function FilterBar({ filters, onChange, showPlant = true, showRange = true, onlySites = false, pinnedPlant, className, children }: {
  filters: Filters;
  onChange: (patch: Partial<Filters>) => void;
  showPlant?: boolean;
  showRange?: boolean;
  /** Offer production SITEs only (production / attendance screens; STK-01). */
  onlySites?: boolean;
  pinnedPlant?: string | null;
  className?: string;
  children?: React.ReactNode;
}) {
  const { data: lookups } = useLookups();
  const { t } = useT();
  const today = todayISO();
  const presets = [
    { label: t("common.today"), from: today, to: today },
    { label: t("common.days7"), from: shiftISO(today, -6), to: today },
    { label: t("common.month"), from: monthStartISO(), to: today },
  ];
  return (
    <div data-tour="filter-bar" className={cn("flex flex-wrap items-center gap-2", className)}>
      {showRange ? (
        <>
          <div className="flex items-center gap-1 rounded-xl border border-line bg-white px-2 shadow-[0_1px_0_rgb(12_35_75/0.02)]">
            <DateInput variant="bare" value={filters.from} max={filters.to} onChange={(v) => v && onChange({ from: v })} aria-label={t("common.fromDate")} />
            <span className="text-slate-300">–</span>
            <DateInput variant="bare" value={filters.to} min={filters.from} onChange={(v) => v && onChange({ to: v })} aria-label={t("common.toDate")} />
          </div>
          <div className="hidden gap-1 sm:flex">
            {presets.map((p) => (
              <button
                key={p.label}
                onClick={() => onChange({ from: p.from, to: p.to })}
                className={cn("h-9 rounded-lg px-3 text-xs font-semibold transition", filters.from === p.from && filters.to === p.to ? "bg-navy-800 text-white" : "text-slate-600 hover:bg-white")}
              >
                {p.label}
              </button>
            ))}
          </div>
        </>
      ) : null}
      {showPlant ? (
        <Select value={filters.plantId} onChange={(e) => onChange({ plantId: e.target.value })} disabled={!!pinnedPlant} className="h-10 w-40" aria-label={t("common.plant")}>
          {!pinnedPlant ? <option value="">{t("common.allPlants")}</option> : null}
          {(onlySites ? sitesOnly(lookups?.plants) : lookups?.plants ?? []).map((p) => <option key={p.id} value={p.id}>{p.code}</option>)}
        </Select>
      ) : null}
      {children}
      <DataCutoff plantId={pinnedPlant ?? filters.plantId} compact className="basis-full sm:ml-auto sm:basis-auto" />
    </div>
  );
}

/** Opens a server-rendered PDF report with the current filters. */
/**
 * Server-rendered PDF report button. The main part opens the report in the user's remembered page
 * shape; the arrow opens a menu to pick Auto / Portrait / Landscape and open or download.
 */
export function PdfButton({ path, params, label, variant = "secondary" }: { path: string; params?: Record<string, string | undefined>; label?: string; variant?: "secondary" | "primary" | "accent" }) {
  const { t } = useT();
  const [shape, setShape] = usePdfShape();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const full = `${path}${qs(params ?? {})}`;

  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent) => ref.current && !ref.current.contains(e.target as Node) && setOpen(false);
    const esc = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    document.addEventListener("mousedown", close);
    document.addEventListener("keydown", esc);
    return () => {
      document.removeEventListener("mousedown", close);
      document.removeEventListener("keydown", esc);
    };
  }, [open]);

  return (
    <div ref={ref} className="relative inline-flex" data-tour="pdf-button">
      <Button variant={variant} icon={<FileDown className="size-4" />} onClick={() => openPdf(full, shape)} className="rounded-r-none">
        {label ?? t("common.pdfReport")}
      </Button>
      <Button
        variant={variant}
        aria-label={t("common.pdfOptions")}
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        className="-ml-px rounded-l-none px-2.5"
      >
        <ChevronDown className={cn("size-4 transition-transform", open && "rotate-180")} />
      </Button>
      {open ? (
        <div role="dialog" aria-label={t("common.pdfOptions")} className="absolute top-full right-0 z-40 mt-2 w-72 rounded-2xl border border-line bg-white p-3.5 shadow-xl">
          <p className="mb-2 text-xs font-semibold tracking-wide text-slate-500 uppercase">{t("common.pageShape")}</p>
          <ShapeToggle value={shape} onChange={setShape} size="sm" />
          <p className="mt-2 text-xs text-slate-500">{t("common.shapeHint")}</p>
          <div className="mt-3 grid grid-cols-2 gap-2 border-t border-line pt-3">
            <Button size="sm" icon={<ExternalLink className="size-3.5" />} onClick={() => { setOpen(false); openPdf(full, shape); }}>
              {t("common.openPdf")}
            </Button>
            <Button size="sm" variant="secondary" icon={<Download className="size-3.5" />} onClick={() => { setOpen(false); downloadPdf(full, shape); }}>
              {t("common.download")}
            </Button>
          </div>
        </div>
      ) : null}
    </div>
  );
}
