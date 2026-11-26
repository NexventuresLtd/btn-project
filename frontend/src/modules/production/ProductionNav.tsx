import { NavLink } from "react-router";
import { BarChart3, CalendarCheck2, ClipboardList, FlaskConical, GitPullRequestArrow, Target } from "lucide-react";
import { cn } from "@/lib/cn";
import { useT } from "@/i18n";

const ITEMS = [
  { to: "/production", end: true, label: "production.nav.entries", Icon: ClipboardList },
  { to: "/production/summary", label: "production.nav.summary", Icon: BarChart3 },
  { to: "/production/targets", label: "production.nav.targets", Icon: Target },
  { to: "/production/days", label: "production.nav.days", Icon: CalendarCheck2 },
  { to: "/production/corrections", label: "production.nav.corrections", Icon: GitPullRequestArrow },
  { to: "/production/recipes", label: "production.nav.recipes", Icon: FlaskConical },
] as const;

/** Sub-navigation of the production module (entries, summaries, targets, day close, corrections, recipes). */
export function ProductionNav() {
  const { t } = useT();
  return (
    <nav aria-label={t("production.nav.label")} className="-mt-2 mb-5 max-w-full overflow-x-auto" data-tour="production-nav">
      <ul className="flex w-max gap-1 rounded-xl bg-slate-100 p-1">
        {ITEMS.map(({ to, label, Icon, ...rest }) => (
          <li key={to}>
            <NavLink
              to={to}
              end={"end" in rest}
              className={({ isActive }) => cn("flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-sm font-semibold whitespace-nowrap transition focus-visible:ring-2 focus-visible:ring-navy-400 focus-visible:outline-none",
                isActive ? "bg-white text-navy-800 shadow-sm" : "text-slate-600 hover:text-slate-900")}
            >
              <Icon className="size-4" aria-hidden />
              {t(label)}
            </NavLink>
          </li>
        ))}
      </ul>
    </nav>
  );
}
