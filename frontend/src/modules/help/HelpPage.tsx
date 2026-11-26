import { useMemo, useState } from "react";
import { useNavigate } from "react-router";
import { ArrowRight, Compass, Search, Sparkles } from "lucide-react";
import { Button, Card, EmptyState, Input, PageHeader } from "@/components/ui";
import { useAuth } from "@/auth/AuthProvider";
import { useT } from "@/i18n";
import { cn } from "@/lib/cn";
import { HELP_GUIDES, stepAllowed, useGuide } from "@/components/guide";
import { HELP_TOPICS, type HelpGuide, type HelpTopic } from "@/components/guide/steps";

interface ShownGuide {
  g: HelpGuide;
  title: string;
  summary: string;
  steps: string[];
}

export function HelpPage() {
  const { t, tx } = useT();
  const { can } = useAuth();
  const { startTour } = useGuide();
  const navigate = useNavigate();
  const [q, setQ] = useState("");

  /** Guides the user may see, filtered by the search text, grouped by topic in HELP_TOPICS order. */
  const groups = useMemo(() => {
    const needle = q.trim().toLowerCase();
    const shown: ShownGuide[] = HELP_GUIDES.filter((g) => stepAllowed(g.perm, can))
      .map((g) => ({
        g,
        title: tx(`guide.help.${g.id}.title`),
        summary: tx(`guide.help.${g.id}.summary`),
        steps: Array.from({ length: g.steps }, (_, i) => tx(`guide.help.${g.id}.s${i + 1}`)),
      }))
      .filter((x) => !needle || [x.title, x.summary, ...x.steps].some((s) => s.toLowerCase().includes(needle)));
    return HELP_TOPICS.map((topic) => ({ topic, guides: shown.filter((x) => x.g.topic === topic) })).filter((x) => x.guides.length > 0);
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

      <div className="relative mb-6">
        <Search className="pointer-events-none absolute top-1/2 left-3.5 size-4 -translate-y-1/2 text-slate-400" />
        <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("guide.help.search")} aria-label={t("guide.help.search")} className="h-11 pl-10" type="search" />
      </div>

      {!groups.length ? (
        <Card><EmptyState icon={<Search />} title={t("guide.help.none")} /></Card>
      ) : (
        <div className="space-y-8">
          {groups.map(({ topic, guides }) => (
            <TopicSection key={topic} topic={topic} guides={guides} onOpen={(to) => navigate(to)} />
          ))}
        </div>
      )}
    </div>
  );
}

function TopicSection({ topic, guides, onOpen }: { topic: HelpTopic; guides: ShownGuide[]; onOpen: (to: string) => void }) {
  const { t, tx } = useT();
  const start = topic === "start";
  const headingId = `help-topic-${topic}`;
  return (
    <section aria-labelledby={headingId} data-testid={`help-topic-${topic}`} className={cn(start && "rounded-2xl border border-navy-100 bg-navy-50/60 p-4 sm:p-5")}>
      <div className="mb-3 flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <h2 id={headingId} className="flex items-center gap-2 text-base font-semibold text-slate-900">
          {start && <Sparkles className="size-4 text-navy-700" aria-hidden="true" />}
          {tx(`guide.topic.${topic}`)}
        </h2>
        {start && <p className="text-[13px] text-slate-600">{t("guide.help.startHint")}</p>}
      </div>
      <div className="grid gap-4 md:grid-cols-2">
        {guides.map(({ g, title, summary, steps }) => (
          <Card key={g.id} className={cn("flex flex-col p-5", start && "border-navy-200")} data-testid={`guide-${g.id}`}>
            <h3 className="text-[15px] font-semibold text-slate-900">{title}</h3>
            <p className="mt-0.5 text-[13px] text-slate-500">{summary}</p>
            <ol className="mt-4 flex-1 space-y-2.5">
              {steps.map((s, i) => (
                <li key={i} className="flex gap-3 text-sm text-slate-700">
                  <span aria-hidden="true" className="mt-0.5 grid size-5 shrink-0 place-items-center rounded-full bg-navy-50 text-[11px] font-bold text-navy-700">{i + 1}</span>
                  <span className="min-w-0">{s}</span>
                </li>
              ))}
            </ol>
            <div className="mt-5 flex items-center justify-between gap-3">
              <span className="text-xs text-slate-400">{t("guide.help.stepsCount", { count: g.steps })}</span>
              <Button size="sm" variant="secondary" icon={<ArrowRight className="size-3.5" />} onClick={() => onOpen(g.to)} aria-label={`${t("guide.help.go")}: ${title}`}>{t("guide.help.go")}</Button>
            </div>
          </Card>
        ))}
      </div>
    </section>
  );
}
