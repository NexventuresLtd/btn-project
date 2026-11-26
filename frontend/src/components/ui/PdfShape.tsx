import { useCallback, useSyncExternalStore } from "react";
import type { PdfShape } from "@/lib/api";
import { useT } from "@/i18n";
import { cn } from "@/lib/cn";
import { radioGroupKeys } from "./radioGroup";

const KEY = "btn.pdfShape";
const SHAPES: PdfShape[] = ["auto", "portrait", "landscape"];
const listeners = new Set<() => void>();

function read(): PdfShape {
  try {
    const v = localStorage.getItem(KEY);
    return v === "portrait" || v === "landscape" ? v : "auto";
  } catch {
    return "auto";
  }
}

function subscribe(fn: () => void) {
  listeners.add(fn);
  const onStorage = (e: StorageEvent) => e.key === KEY && fn();
  window.addEventListener("storage", onStorage);
  return () => {
    listeners.delete(fn);
    window.removeEventListener("storage", onStorage);
  };
}

/** The user's preferred PDF page shape, remembered in this browser and shared by every PDF button. */
export function usePdfShape(): [PdfShape, (s: PdfShape) => void] {
  const shape = useSyncExternalStore(subscribe, read, () => "auto" as PdfShape);
  const set = useCallback((s: PdfShape) => {
    try {
      if (s === "auto") localStorage.removeItem(KEY);
      else localStorage.setItem(KEY, s);
    } catch {
      /* storage unavailable: the choice still applies to this click */
    }
    listeners.forEach((l) => l());
  }, []);
  return [shape, set];
}

/** Portrait / landscape page glyph. */
function ShapeIcon({ shape, className }: { shape: PdfShape; className?: string }) {
  if (shape === "auto") {
    return (
      <svg viewBox="0 0 16 16" className={className} aria-hidden="true">
        <rect x="1.5" y="3.5" width="7" height="10" rx="1" fill="none" stroke="currentColor" strokeWidth="1.3" />
        <rect x="6.5" y="1.5" width="8" height="6" rx="1" fill="currentColor" fillOpacity=".15" stroke="currentColor" strokeWidth="1.3" />
      </svg>
    );
  }
  const land = shape === "landscape";
  return (
    <svg viewBox="0 0 16 16" className={className} aria-hidden="true">
      <rect x={land ? 1.5 : 3.5} y={land ? 3.5 : 1.5} width={land ? 13 : 9} height={land ? 9 : 13} rx="1.2" fill="none" stroke="currentColor" strokeWidth="1.4" />
    </svg>
  );
}

/** Segmented Auto / Portrait / Landscape control. */
export function ShapeToggle({ value, onChange, className, size = "md" }: { value: PdfShape; onChange: (s: PdfShape) => void; className?: string; size?: "sm" | "md" }) {
  const { t } = useT();
  const label: Record<PdfShape, string> = { auto: t("common.shapeAuto"), portrait: t("common.portrait"), landscape: t("common.landscape") };
  const rg = radioGroupKeys(SHAPES, value, onChange);
  return (
    <div role="radiogroup" aria-label={t("common.pageShape")} onKeyDown={rg.onKeyDown} className={cn("inline-flex w-full rounded-xl bg-slate-100 p-1", className)}>
      {SHAPES.map((s) => (
        <button
          key={s}
          type="button"
          role="radio"
          aria-checked={value === s}
          tabIndex={rg.tabIndex(s)}
          data-value={s}
          onClick={() => onChange(s)}
          className={cn(
            "flex flex-1 items-center justify-center gap-1.5 rounded-lg font-semibold whitespace-nowrap transition",
            size === "sm" ? "px-2 py-1 text-xs" : "px-2.5 py-1.5 text-[13px]",
            value === s ? "bg-white text-navy-800 shadow-sm" : "text-slate-500 hover:text-slate-800",
          )}
        >
          <ShapeIcon shape={s} className="size-3.5 shrink-0" />
          {label[s]}
        </button>
      ))}
    </div>
  );
}
