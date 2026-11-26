import { useEffect, useRef, useState } from "react";
import { ChevronDown, FileJson, FileSpreadsheet, FileText, Sheet } from "lucide-react";
import { getLang, useT } from "@/i18n";
import { downloadFile, qs } from "@/lib/api";
import { cn } from "@/lib/cn";
import { Button } from "./Button";

export type ExportFormat = "csv" | "xlsx" | "json";

/** URL path of a report export: `<path>/export?format=…&lang=…&<filters>` (see backend internal/export). */
export function exportPath(path: string, format: ExportFormat, params?: Record<string, string | number | undefined | null>) {
  const [base, query] = path.split("?");
  const extra = Object.fromEntries(new URLSearchParams(query ?? ""));
  return `${base.replace(/\/$/, "")}/export${qs({ ...extra, ...(params ?? {}), format, lang: getLang() })}`;
}

const FORMATS: { id: ExportFormat; icon: typeof FileText }[] = [
  { id: "csv", icon: FileText },
  { id: "xlsx", icon: FileSpreadsheet },
  { id: "json", icon: FileJson },
];

/**
 * Export the report behind `path` (a JSON report/list endpoint registered with the backend export framework)
 * as CSV, Excel or JSON with the current filters and language. Sits next to every PdfButton and on list pages.
 */
export function ExportMenu({ path, params, label, size = "md", className }: {
  path: string;
  params?: Record<string, string | number | undefined | null>;
  label?: string;
  size?: "sm" | "md";
  className?: string;
}) {
  const { t } = useT();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const items = useRef<(HTMLButtonElement | null)[]>([]);

  useEffect(() => {
    if (!open) return;
    items.current[0]?.focus();
    const close = (e: MouseEvent) => ref.current && !ref.current.contains(e.target as Node) && setOpen(false);
    const key = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        const i = items.current.findIndex((b) => b === document.activeElement);
        const n = FORMATS.length;
        items.current[(i + (e.key === "ArrowDown" ? 1 : n - 1) + n) % n]?.focus();
      }
    };
    document.addEventListener("mousedown", close);
    document.addEventListener("keydown", key);
    return () => {
      document.removeEventListener("mousedown", close);
      document.removeEventListener("keydown", key);
    };
  }, [open]);

  const pick = (f: ExportFormat) => {
    setOpen(false);
    downloadFile(exportPath(path, f, params));
  };

  return (
    <div ref={ref} className={cn("relative inline-flex", className)} data-tour="export-menu">
      <Button variant="secondary" size={size} icon={<Sheet className="size-4" />} aria-haspopup="menu" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
        {label ?? t("exports.menu.label")}
        <ChevronDown className={cn("size-4 transition-transform", open && "rotate-180")} />
      </Button>
      {open ? (
        <div role="menu" aria-label={t("exports.menu.label")} className="absolute top-full right-0 z-40 mt-2 w-64 rounded-2xl border border-line bg-white p-1.5 shadow-xl">
          {FORMATS.map((f, i) => (
            <button
              key={f.id}
              ref={(el) => { items.current[i] = el; }}
              role="menuitem"
              type="button"
              onClick={() => pick(f.id)}
              className="flex w-full items-start gap-3 rounded-xl px-3 py-2 text-left outline-none hover:bg-slate-50 focus-visible:bg-navy-50 focus-visible:ring-2 focus-visible:ring-navy-200"
            >
              <f.icon className="mt-0.5 size-4 shrink-0 text-navy-700" aria-hidden="true" />
              <span>
                <span className="block text-sm font-semibold text-slate-800">{t(`exports.menu.${f.id}`)}</span>
                <span className="block text-xs text-slate-500">{t(`exports.menu.${f.id}Hint`)}</span>
              </span>
            </button>
          ))}
          <p className="border-t border-line px-3 pt-2 pb-1.5 text-[11px] text-slate-500">{t("exports.menu.hint")}</p>
        </div>
      ) : null}
    </div>
  );
}
