import { useMemo, useState } from "react";
import { useNavigate } from "react-router";
import { ArrowRight, Compass, Search } from "lucide-react";
import { Button, Card, EmptyState, Input, PageHeader } from "@/components/ui";
import { useAuth } from "@/auth/AuthProvider";
import { useT } from "@/i18n";
import { HELP_GUIDES, stepAllowed, useGuide } from "@/components/guide";

export function HelpPage() {
  const { t, tx } = useT();
  const { can } = useAuth();
  const { startTour } = useGuide();
  const navigate = useNavigate();
  const [q, setQ] = useState("");

  const guides = useMemo(() => {
    const needle = q.trim().toLowerCase();
    return HELP_GUIDES.filter((g) => stepAllowed(g.perm, can))
      .map((g) => {
        const title = tx(`guide.help.${g.id}.title`);
        const summary = tx(`guide.help.${g.id}.summary`);
        const steps = Array.from({ length: g.steps }, (_, i) => tx(`guide.help.${g.id}.s${i + 1}`));
        return { g, title, summary, steps };
      })
      .filter((x) => !needle || [x.title, x.summary, ...x.steps].some((s) => s.toLowerCase().includes(needle)));
  }, [q, can, tx]);

  return (
    <div className="mx-auto max-w-4xl">
      <PageHeader title={t("guide.help.title")} description={t("guide.help.description")} />
      <Card className="mb-6 flex flex-wrap items-center gap-4 p-5">
        <span className="grid size-11 shrink-0 place-items-center rounded-xl bg-navy-50 text-navy-700"><Compass className="size-5" /></span>
        <div className="min-w-0 flex-1 basis-64">
          <p className="font-semibold text-slate-900">{t("guide.help.tourTitle")}</p>
          <p className="text-sm text-slate-500">{t("guide.help.tourText")}</p>
        </div>
        <Button onClick={startTour}>{t("guide.help.tourButton")}</Button>
      </Card>

      <div className="relative mb-5">
        <Search className="pointer-events-none absolute top-1/2 left-3.5 size-4 -translate-y-1/2 text-slate-400" />
        <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("guide.help.search")} aria-label={t("guide.help.search")} className="h-11 pl-10" type="search" />
      </div>

      {!guides.length ? (
        <Card><EmptyState icon={<Search />} title={t("guide.help.none")} /></Card>
      ) : (
        <div className="grid gap-4 md:grid-cols-2">
          {guides.map(({ g, title, summary, steps }) => (
            <Card key={g.id} className="flex flex-col p-5" data-testid={`guide-${g.id}`}>
              <h2 className="text-[15px] font-semibold text-slate-900">{title}</h2>
              <p className="mt-0.5 text-[13px] text-slate-500">{summary}</p>
              <ol className="mt-4 flex-1 space-y-2.5">
                {steps.map((s, i) => (
                  <li key={i} className="flex gap-3 text-sm text-slate-700">
                    <span className="mt-0.5 grid size-5 shrink-0 place-items-center rounded-full bg-navy-50 text-[11px] font-bold text-navy-700">{i + 1}</span>
                    <span className="min-w-0">{s}</span>
                  </li>
                ))}
              </ol>
              <div className="mt-5 flex items-center justify-between gap-3">
                <span className="text-xs text-slate-400">{t("guide.help.stepsCount", { count: g.steps })}</span>
                <Button size="sm" variant="secondary" icon={<ArrowRight className="size-3.5" />} onClick={() => navigate(g.to)}>{t("guide.help.go")}</Button>
              </div>
            </Card>
          ))}
        </div>
      )}
    </div>
  );
}
