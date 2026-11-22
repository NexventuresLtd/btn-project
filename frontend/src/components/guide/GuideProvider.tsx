import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useLocation, useNavigate } from "react-router";
import { Compass } from "lucide-react";
import { useAuth } from "@/auth/AuthProvider";
import { useT } from "@/i18n";
import { Button } from "@/components/ui/Button";
import { useLayout } from "@/layouts/LayoutContext";
import { findTarget, TourOverlay } from "./TourOverlay";
import { stepAllowed, TOUR_STEPS, type TourStep } from "./steps";
import { getGuideStorage } from "./storage";

interface GuideCtx {
  startTour: () => void;
}
const Ctx = createContext<GuideCtx>({ startTour: () => {} });
export const useGuide = () => useContext(Ctx);

export const USER_MENU_EVENT = "btn:user-menu";
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

/** Welcome card (first visit) + spotlight tour. Role-aware via permissions; missing targets are skipped. */
export function GuideProvider({ children }: { children: ReactNode }) {
  const { user, can } = useAuth();
  const { t } = useT();
  const navigate = useNavigate();
  const location = useLocation();
  const layout = useLayout();
  const pathRef = useRef(location.pathname);
  pathRef.current = location.pathname;
  const [welcome, setWelcome] = useState(false);
  const [run, setRun] = useState<{ steps: TourStep[]; index: number } | null>(null);
  const runRef = useRef(run);
  runRef.current = run;
  const token = useRef(0);

  const prepare = useCallback(async (step: TourStep, tk: number): Promise<boolean> => {
    if (step.route && pathRef.current !== step.route) navigate(step.route);
    const needsNav = step.target === "sidebar" || step.target.startsWith("nav:");
    if (layout.isMobile()) { if (needsNav) layout.openMobileNav(); else layout.closeMobileNav(); }
    window.dispatchEvent(new CustomEvent(USER_MENU_EVENT, { detail: !!step.userMenu }));
    for (let i = 0; i < 24; i++) {
      if (tk !== token.current) return false;
      if (findTarget(step.target)) return true;
      await sleep(100);
    }
    return false;
  }, [navigate, layout]);

  const show = useCallback(async (steps: TourStep[], from: number, dir: 1 | -1) => {
    const tk = ++token.current;
    let list = steps;
    let i = from;
    while (i >= 0 && i < list.length) {
      if (await prepare(list[i]!, tk)) {
        if (tk === token.current) setRun({ steps: list, index: i });
        return;
      }
      if (tk !== token.current) return;
      list = list.filter((_, k) => k !== i);
      if (dir < 0) i--;
    }
    setRun(null);
    window.dispatchEvent(new CustomEvent(USER_MENU_EVENT, { detail: false }));
  }, [prepare]);

  const startTour = useCallback(() => {
    setWelcome(false);
    const steps = TOUR_STEPS.filter((s) => stepAllowed(s.perm, can));
    void show(steps, 0, 1);
  }, [can, show]);

  const close = useCallback((finished: boolean) => {
    void finished;
    token.current++;
    setRun(null);
    window.dispatchEvent(new CustomEvent(USER_MENU_EVENT, { detail: false }));
    if (layout.isMobile()) layout.closeMobileNav();
  }, [layout]);

  const onIndex = useCallback((i: number) => {
    const r = runRef.current;
    if (!r) return;
    void show(r.steps, i, i > r.index ? 1 : -1);
  }, [show]);

  // first visit to the dashboard: offer the tour once per user (never auto-start twice)
  const uid = user?.id;
  useEffect(() => {
    if (!uid || user?.mustChangePassword || location.pathname !== "/" || !can("dashboard.view")) return;
    const store = getGuideStorage();
    if (store.hasSeen(uid)) return;
    store.markSeen(uid);
    const id = window.setTimeout(() => setWelcome(true), 700);
    return () => window.clearTimeout(id);
  }, [uid, user?.mustChangePassword, location.pathname, can]);

  const value = useMemo(() => ({ startTour }), [startTour]);
  return (
    <Ctx.Provider value={value}>
      {children}
      {welcome ? <WelcomeCard name={user?.name.split(" ")[0] ?? ""} onStart={startTour} onSkip={() => setWelcome(false)} /> : null}
      {run ? <TourOverlay steps={run.steps} index={run.index} onIndex={onIndex} onClose={close} /> : null}
      <span className="sr-only" aria-live="polite">{run ? t("guide.progress", { n: run.index + 1, total: run.steps.length }) : ""}</span>
    </Ctx.Provider>
  );
}

function WelcomeCard({ name, onStart, onSkip }: { name: string; onStart: () => void; onSkip: () => void }) {
  const { t } = useT();
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    ref.current?.querySelector<HTMLElement>("[data-autofocus]")?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onSkip();
      if (e.key === "Tab") {
        const f = ref.current?.querySelectorAll<HTMLElement>("button");
        if (!f?.length) return;
        const first = f[0]!, last = f[f.length - 1]!;
        if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
        else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
      }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onSkip]);
  return (
    <div className="fixed inset-0 z-[70] grid place-items-center bg-navy-950/55 p-4 backdrop-blur-[2px]">
      <div ref={ref} role="dialog" aria-modal="true" aria-labelledby="welcome-title" aria-describedby="welcome-text" className="w-full max-w-md rounded-2xl bg-white p-7 text-center shadow-2xl">
        <span className="mx-auto mb-4 grid size-14 place-items-center rounded-2xl bg-navy-50 text-navy-700 ring-1 ring-navy-100"><Compass className="size-7" /></span>
        <h2 id="welcome-title" className="text-xl font-bold text-slate-900">{t("guide.welcome.title", { name })}</h2>
        <p id="welcome-text" className="mt-2 text-sm leading-relaxed text-slate-600">{t("guide.welcome.text")}</p>
        <div className="mt-6 flex flex-col-reverse gap-2 sm:flex-row sm:justify-center">
          <Button variant="secondary" onClick={onSkip}>{t("guide.welcome.skip")}</Button>
          <Button data-autofocus onClick={onStart}>{t("guide.welcome.start")}</Button>
        </div>
        <p className="mt-4 text-xs text-slate-400">{t("guide.welcome.hint")}</p>
      </div>
    </div>
  );
}
