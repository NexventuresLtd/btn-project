import { Suspense, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, NavLink, Outlet, useLocation, useNavigate } from "react-router";
import { ChevronDown, CircleHelp, Compass, Eye, EyeOff, KeyRound, LifeBuoy, LogOut, Menu, ShieldCheck, X } from "lucide-react";
import { ViewAsBar, ViewAsDialog } from "@/components/ViewAs";
import { NAV } from "@/app/nav";
import { prefetchRoute } from "@/app/prefetch";
import { useAuth } from "@/auth/AuthProvider";
import { cn } from "@/lib/cn";
import { formatDate, todayISO } from "@/lib/dates";
import { useBranding } from "@/branding";
import { useT } from "@/i18n";
import { PageLoader } from "@/components/ui";
import { LanguageSwitcher } from "@/components/LanguageSwitcher";
import { ConnectionIndicator } from "@/offline/ui/ConnectionIndicator";
import { SyncNavBadge } from "@/offline/ui/SyncNavBadge";
import { OfflineDataBanner } from "@/offline/ui/OfflineDataBanner";
import { OnlineOnlyZone, isOnlineOnlyPath } from "@/offline/onlineOnly";
import { GuideProvider, useGuide, USER_MENU_EVENT } from "@/components/guide";
import { NotificationBell } from "@/modules/notifications/NotificationBell";
import { BellRing } from "lucide-react";
import { MonitorSmartphone } from "lucide-react"; // F3: my devices
import { LayoutContext } from "./LayoutContext";

function initials(name: string) {
  return name.split(/\s+/).filter(Boolean).slice(0, 2).map((p) => p[0]!.toUpperCase()).join("");
}

function Sidebar({ onNavigate }: { onNavigate?: () => void }) {
  const { can } = useAuth();
  const { t } = useT();
  const b = useBranding();
  return (
    <div data-tour="sidebar" className="flex h-full flex-col bg-navy-800 text-navy-100">
      <Link to="/" onClick={onNavigate} className="flex h-[72px] shrink-0 items-center px-6">
        <img src={b.logoDarkUrl} alt={b.name} className="h-9 w-auto max-w-full object-contain" />
      </Link>
      <nav aria-label={t("layout.mainNav")} className="flex-1 space-y-6 overflow-y-auto px-3 pt-2 pb-6">
        {NAV.map((group) => {
          const items = group.items.filter((i) => i.permission === "*" || (Array.isArray(i.permission) ? i.permission : [i.permission]).some(can));
          if (!items.length) return null;
          return (
            <div key={group.id} data-tour={`nav-group-${group.id}`}>
              <p className="mb-1.5 px-3 text-[10.5px] font-semibold tracking-[0.12em] text-navy-200 uppercase">{t(group.labelKey)}</p>
              <ul className="space-y-0.5">
                {items.map(({ to, labelKey, icon: Icon }) => (
                  <li key={to}>
                    <NavLink
                      to={to}
                      data-tour-nav={to}
                      end={to === "/" || to === "/sales" || to === "/payroll" || to === "/finance"}
                      onClick={onNavigate}
                      onMouseEnter={() => prefetchRoute(to)}
                      onFocus={() => prefetchRoute(to)}
                      className={({ isActive }) =>
                        cn(
                          "group relative flex items-center gap-3 rounded-xl px-3 py-2.5 text-[13.5px] leading-snug font-medium transition-colors",
                          isActive ? "bg-white/10 text-white" : "text-navy-200/85 hover:bg-white/5 hover:text-white",
                        )
                      }
                    >
                      {({ isActive }) => (
                        <>
                          {isActive ? <span className="absolute top-2 bottom-2 left-0 w-[3px] rounded-r-full bg-accent" /> : null}
                          <Icon className={cn("size-[18px] shrink-0", isActive ? "text-accent" : "text-navy-300 group-hover:text-navy-100")} />
                          <span className="min-w-0">{t(labelKey)}</span>
                          {to === "/sync-issues" ? <SyncNavBadge /> : null}
                        </>
                      )}
                    </NavLink>
                  </li>
                ))}
              </ul>
            </div>
          );
        })}
      </nav>
      <div className="mx-3 mb-4 rounded-xl bg-white/5 p-3.5 ring-1 ring-white/10">
        <p className="text-[11px] font-semibold text-navy-200">{t("common.appName")}</p>
        <p className="mt-0.5 text-[11px] text-navy-200">{t("layout.sidebar.kigali", { date: formatDate(todayISO()) })}</p>
      </div>
    </div>
  );
}

function UserMenu() {
  const { user, logout, exitViewAs } = useAuth();
  const { t, tx } = useT();
  const [viewAsOpen, setViewAsOpen] = useState(false);
  const { startTour } = useGuide();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const close = (e: MouseEvent) => ref.current && !ref.current.contains(e.target as Node) && setOpen(false);
    const onTour = (e: Event) => setOpen(!!(e as CustomEvent<boolean>).detail);
    const esc = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    document.addEventListener("mousedown", close);
    document.addEventListener("keydown", esc);
    window.addEventListener(USER_MENU_EVENT, onTour);
    return () => {
      document.removeEventListener("mousedown", close);
      document.removeEventListener("keydown", esc);
      window.removeEventListener(USER_MENU_EVENT, onTour);
    };
  }, []);
  if (!user) return null;
  const go = (to: string) => { setOpen(false); navigate(to); };
  const item = "flex w-full items-center gap-2.5 px-4 py-2.5 text-left text-sm text-slate-700 hover:bg-slate-50";
  return (
    <div className="relative" ref={ref} data-tour="user-menu">
      <button onClick={() => setOpen((o) => !o)} aria-haspopup="menu" aria-expanded={open} aria-label={t("layout.menu.account")} className="flex items-center gap-3 rounded-xl py-1.5 pr-2 pl-1.5 transition hover:bg-slate-100">
        <span className="grid size-9 place-items-center rounded-xl bg-navy-800 text-[13px] font-bold text-white">{initials(user.name)}</span>
        <span className="hidden max-w-[14rem] text-left sm:block">
          <span className="block truncate text-[13px] leading-tight font-semibold text-slate-800">{user.name}</span>
          <span className="block truncate text-[11.5px] leading-tight text-slate-500">{tx(`enums.role.${user.role}`, undefined, user.roleLabel)}{user.plant ? ` · ${user.plant.code}` : ""}</span>
        </span>
        <ChevronDown className="size-4 text-slate-400" />
      </button>
      {open ? (
        <div role="menu" className="absolute right-0 z-30 mt-2 w-72 max-w-[calc(100vw-1rem)] overflow-hidden rounded-2xl border border-line bg-white shadow-xl">
          <div className="border-b border-line px-4 py-3">
            <p className="truncate text-sm font-semibold text-slate-800">{user.name}</p>
            <p className="truncate text-xs text-slate-500">{user.email}</p>
          </div>
          <button role="menuitem" data-tour="menu-security" onClick={() => go("/account/security")} className={item}><ShieldCheck className="size-4 shrink-0 text-slate-400" /> {t("layout.menu.security")}</button>
          <button role="menuitem" onClick={() => go("/account/devices")} className={item}><MonitorSmartphone className="size-4 shrink-0 text-slate-400" /> {t("session.menu.devices")}</button>
          <button role="menuitem" onClick={() => go("/notifications/preferences")} className={item}><BellRing className="size-4 shrink-0 text-slate-400" /> {t("notifications.settings")}</button>
          <button role="menuitem" onClick={() => go("/account/password")} className={item}><KeyRound className="size-4 shrink-0 text-slate-400" /> {t("layout.menu.changePassword")}</button>
          <button role="menuitem" onClick={() => { setOpen(false); startTour(); }} className={item}><Compass className="size-4 shrink-0 text-slate-400" /> {t("layout.menu.takeTour")}</button>
          <button role="menuitem" onClick={() => go("/help")} className={item}><LifeBuoy className="size-4 shrink-0 text-slate-400" /> {t("layout.menu.help")}</button>
          {user.viewAs ? (
            <button role="menuitem" onClick={() => { setOpen(false); void exitViewAs().then(() => navigate("/")); }} className={item}><EyeOff className="size-4 shrink-0 text-slate-400" /> {t("layout.viewAs.exit")}</button>
          ) : user.role === "SUPER_ADMIN" ? (
            <button role="menuitem" onClick={() => { setOpen(false); setViewAsOpen(true); }} className={item}><Eye className="size-4 shrink-0 text-slate-400" /> {t("layout.viewAs.menu")}</button>
          ) : null}
          <button role="menuitem" onClick={() => void logout().then((ok) => ok && navigate("/login"))} className="flex w-full items-center gap-2.5 border-t border-line px-4 py-2.5 text-left text-sm text-red-600 hover:bg-red-50"><LogOut className="size-4 shrink-0" /> {t("layout.menu.signOut")}</button>
        </div>
      ) : null}
      <ViewAsDialog open={viewAsOpen} onClose={() => setViewAsOpen(false)} />
    </div>
  );
}

function GuideButton() {
  const { t } = useT();
  const { startTour } = useGuide();
  return (
    <button onClick={startTour} data-tour="guide-button" aria-label={t("layout.guide")} title={t("layout.guide")} className="grid size-9 place-items-center rounded-xl text-slate-600 transition hover:bg-slate-100">
      <CircleHelp className="size-[18px]" />
    </button>
  );
}

export function AppLayout() {
  const [mobileOpen, setMobileOpen] = useState(false);
  const location = useLocation();
  const { t } = useT();
  useEffect(() => setMobileOpen(false), [location.pathname]);
  const ctx = useMemo(() => ({
    openMobileNav: () => setMobileOpen(true),
    closeMobileNav: () => setMobileOpen(false),
    isMobile: () => window.matchMedia("(max-width: 1023px)").matches,
  }), []);
  const closeNav = useCallback(() => setMobileOpen(false), []);

  return (
    <LayoutContext.Provider value={ctx}>
      <GuideProvider>
        <div className="min-h-svh">
          <a href="#main" className="sr-only focus:not-sr-only focus:fixed focus:top-2 focus:left-2 focus:z-[80] focus:rounded-lg focus:bg-white focus:px-3 focus:py-2 focus:text-sm focus:shadow-lg">{t("layout.skipToContent")}</a>
          <aside className="fixed inset-y-0 left-0 z-20 hidden w-64 lg:block"><Sidebar /></aside>
          {mobileOpen ? (
            <div className="fixed inset-0 z-40 lg:hidden">
              <div className="absolute inset-0 bg-navy-950/50" onClick={closeNav} />
              <aside className="absolute inset-y-0 left-0 w-72 shadow-2xl"><Sidebar onNavigate={closeNav} /></aside>
              <button onClick={closeNav} className="absolute top-4 left-[300px] rounded-lg bg-white/10 p-2 text-white" aria-label={t("common.closeMenu")}><X className="size-5" /></button>
            </div>
          ) : null}
          <div className="lg:pl-64">
            <ViewAsBar />
            <header className="sticky top-0 z-10 flex h-[72px] items-center justify-between gap-2 border-b border-line bg-white/85 px-3 backdrop-blur-md sm:gap-4 sm:px-8">
              <button onClick={() => setMobileOpen(true)} className="rounded-lg p-2 text-slate-600 hover:bg-slate-100 lg:hidden" aria-label={t("common.openMenu")}><Menu className="size-5" /></button>
              <div className="hidden lg:block" />
              <div className="flex items-center gap-1 sm:gap-2">
                <ConnectionIndicator />
                <NotificationBell />
                <LanguageSwitcher />
                <GuideButton />
                <UserMenu />
              </div>
            </header>
            <main id="main" className="w-full px-4 py-7 sm:px-8">
              <OfflineDataBanner />
              <OnlineOnlyZone enabled={isOnlineOnlyPath(location.pathname)}>
                <Suspense fallback={<PageLoader />}>
                  <Outlet />
                </Suspense>
              </OnlineOnlyZone>
            </main>
          </div>
        </div>
      </GuideProvider>
    </LayoutContext.Provider>
  );
}
