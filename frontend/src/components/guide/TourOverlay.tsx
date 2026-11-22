import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { ArrowLeft, ArrowRight, X } from "lucide-react";
import { useT } from "@/i18n";
import { cn } from "@/lib/cn";
import { Button } from "@/components/ui/Button";

export interface ResolvedStep {
  id: string;
  target: string;
}

/** First visible element for a data-tour id (the desktop + mobile sidebars may both exist). */
export function findTarget(target: string): HTMLElement | null {
  const sel = target.startsWith("nav:") ? `[data-tour-nav="${target.slice(4)}"]` : `[data-tour="${target}"]`;
  for (const el of document.querySelectorAll<HTMLElement>(sel)) {
    const r = el.getBoundingClientRect();
    if (r.width > 0 && r.height > 0) return el;
  }
  return null;
}

const PAD = 8;

export function TourOverlay({ steps, index, onIndex, onClose }: { steps: ResolvedStep[]; index: number; onIndex: (i: number) => void; onClose: (finished: boolean) => void }) {
  const { t, tx } = useT();
  const step = steps[index]!;
  const [rect, setRect] = useState<DOMRect | null>(null);
  const popRef = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState<{ top: number; left: number; width: number } | null>(null);
  const last = index === steps.length - 1;

  const measure = useCallback(() => {
    const el = findTarget(step.target);
    setRect(el ? el.getBoundingClientRect() : null);
  }, [step.target]);

  // scroll the target into view, then track its rect
  useLayoutEffect(() => {
    const el = findTarget(step.target);
    el?.scrollIntoView({ block: "center", inline: "nearest", behavior: "instant" as ScrollBehavior });
    measure();
    const onChange = () => measure();
    window.addEventListener("resize", onChange);
    window.addEventListener("scroll", onChange, true);
    const iv = window.setInterval(onChange, 400); // layout shifts (data loading, menus opening)
    return () => {
      window.removeEventListener("resize", onChange);
      window.removeEventListener("scroll", onChange, true);
      window.clearInterval(iv);
    };
  }, [step.target, measure]);

  // popover placement: below the target if it fits, else above, else centred; bottom sheet on phones
  useLayoutEffect(() => {
    const pop = popRef.current;
    if (!pop) return;
    const vw = window.innerWidth;
    const vh = window.innerHeight;
    const width = Math.min(380, vw - 24);
    const h = pop.offsetHeight;
    if (!rect || vw < 640) {
      setPos({ top: rect && vw < 640 ? (rect.bottom + PAD + h + 12 < vh ? rect.bottom + PAD + 12 : Math.max(12, vh - h - 12)) : Math.max(12, (vh - h) / 2), left: (vw - width) / 2, width });
      return;
    }
    const below = rect.bottom + PAD + 14;
    const above = rect.top - PAD - 14 - h;
    const right = rect.right + PAD + 14;
    let top: number;
    let left: number;
    if (right + width < vw - 12 && rect.height > h * 0.6 && rect.width < vw * 0.4) {
      left = right;
      top = Math.min(Math.max(12, rect.top), vh - h - 12);
    } else {
      top = below + h < vh - 12 ? below : above > 12 ? above : Math.max(12, vh - h - 12);
      left = Math.min(Math.max(12, rect.left + rect.width / 2 - width / 2), vw - width - 12);
    }
    setPos({ top, left, width });
  }, [rect, index]);

  // keyboard + focus trap
  useEffect(() => {
    const prev = document.activeElement as HTMLElement | null;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") { e.preventDefault(); onClose(false); }
      else if (e.key === "ArrowRight") { e.preventDefault(); if (last) onClose(true); else onIndex(index + 1); }
      else if (e.key === "ArrowLeft") { e.preventDefault(); if (index > 0) onIndex(index - 1); }
      else if (e.key === "Tab") {
        const f = popRef.current?.querySelectorAll<HTMLElement>("button:not([disabled])");
        if (!f?.length) return;
        const first = f[0]!;
        const lastEl = f[f.length - 1]!;
        if (e.shiftKey && document.activeElement === first) { e.preventDefault(); lastEl.focus(); }
        else if (!e.shiftKey && document.activeElement === lastEl) { e.preventDefault(); first.focus(); }
        else if (!popRef.current?.contains(document.activeElement)) { e.preventDefault(); first.focus(); }
      }
    };
    document.addEventListener("keydown", onKey, true);
    return () => {
      document.removeEventListener("keydown", onKey, true);
      prev?.focus?.();
    };
  }, [index, last, onClose, onIndex]);

  useEffect(() => {
    popRef.current?.querySelector<HTMLElement>("[data-autofocus]")?.focus();
  }, [index]);

  const titleId = "tour-title";
  const textId = "tour-text";
  return (
    <div className="fixed inset-0 z-[70]" data-tour-overlay>
      {/* click-shield: the page behind is inert while the tour runs */}
      <div className="absolute inset-0" onClick={(e) => e.stopPropagation()} />
      {rect ? (
        <div
          aria-hidden
          className="pointer-events-none absolute rounded-xl ring-2 ring-accent transition-all duration-200"
          style={{ top: rect.top - PAD / 2, left: rect.left - PAD / 2, width: rect.width + PAD, height: rect.height + PAD, boxShadow: "0 0 0 9999px rgb(5 15 36 / 0.62)" }}
        />
      ) : (
        <div aria-hidden className="absolute inset-0 bg-navy-950/60" />
      )}
      <div
        ref={popRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={textId}
        style={pos ? { top: pos.top, left: pos.left, width: pos.width } : { top: 0, left: 0, width: 380, visibility: "hidden" }}
        className="absolute rounded-2xl border border-line bg-white p-5 shadow-2xl"
      >
        <div className="flex items-start justify-between gap-3">
          <h2 id={titleId} className="text-[15px] leading-snug font-semibold text-slate-900">{tx(`guide.step.${step.id}.title`, undefined, step.id)}</h2>
          <button onClick={() => onClose(false)} aria-label={t("guide.skipTour")} className="-m-1 shrink-0 rounded-lg p-1 text-slate-400 hover:bg-slate-100 hover:text-slate-700"><X className="size-4" /></button>
        </div>
        <p id={textId} className="mt-1.5 text-sm leading-relaxed text-slate-600">{tx(`guide.step.${step.id}.text`, undefined, "")}</p>
        <div className="mt-4 flex items-center justify-between gap-3">
          <ol className="flex items-center gap-1.5" aria-label={t("guide.progress", { n: index + 1, total: steps.length })}>
            {steps.map((s, i) => (
              <li key={s.id} aria-current={i === index ? "step" : undefined} className={cn("h-1.5 rounded-full transition-all", i === index ? "w-5 bg-navy-700" : i < index ? "w-1.5 bg-navy-300" : "w-1.5 bg-slate-200")} />
            ))}
          </ol>
          <span className="text-xs text-slate-400 tabular" aria-live="polite">{t("guide.progress", { n: index + 1, total: steps.length })}</span>
        </div>
        <div className="mt-4 flex flex-wrap items-center justify-between gap-2">
          <button onClick={() => onClose(false)} className="text-[13px] font-medium text-slate-500 hover:text-slate-800">{t("guide.skipTour")}</button>
          <div className="flex gap-2">
            {index > 0 ? <Button size="sm" variant="secondary" icon={<ArrowLeft className="size-3.5" />} onClick={() => onIndex(index - 1)}>{t("common.back")}</Button> : null}
            <Button size="sm" data-autofocus onClick={() => (last ? onClose(true) : onIndex(index + 1))}>
              {last ? t("common.done") : t("common.next")} {last ? null : <ArrowRight className="size-3.5" />}
            </Button>
          </div>
        </div>
      </div>
    </div>
  );
}
