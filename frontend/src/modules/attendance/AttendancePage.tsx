import { useMemo } from "react";
import { Link, useSearchParams } from "react-router";
import { useT } from "@/i18n";
import { PageHeader, Tabs } from "@/components/ui";
import { useAuth } from "@/auth/AuthProvider";
import { sitesOnly, useLookups } from "@/hooks/useLookups";
import { DailyTab } from "./DailyTab";
import { SummaryTab } from "./SummaryTab";
import { ApprovalsTab } from "./ApprovalsTab";
import { pinnedPlantId } from "@/lib/scope";


/** Shared site state for both tabs: plant-scoped users are pinned to their own site. */
export function useSiteSelection() {
  const { user } = useAuth();
  const { data: lookups } = useLookups();
  const pinned = pinnedPlantId(user);
  const plants = sitesOnly(lookups?.plants); // attendance only at production sites (STK-01)
  const [params, setParams] = useSearchParams();
  const requested = params.get("plantId") ?? "";
  const plantId = pinned ?? (requested && plants.some((p) => p.id === requested) ? requested : plants[0]?.id ?? "");
  const setPlantId = (id: string) => {
    if (pinned) return;
    const next = new URLSearchParams(params);
    if (id) next.set("plantId", id); else next.delete("plantId");
    setParams(next, { replace: true });
  };
  return { pinned, plants, plantId, setPlantId };
}

type TabKey = "daily" | "summary" | "approvals";

export function AttendancePage() {
  const { t } = useT();
  const site = useSiteSelection();
  const [params, setParams] = useSearchParams();
  const requested = params.get("tab");
  const tab: TabKey = requested === "summary" || requested === "approvals" ? requested : "daily";
  const setTab = (v: TabKey) => {
    const next = new URLSearchParams(params);
    if (v === "daily") next.delete("tab"); else next.set("tab", v);
    setParams(next, { replace: true });
  };
  const tabs = useMemo(() => [
    { value: "daily" as const, label: t("attendance.tab.daily") },
    { value: "summary" as const, label: t("attendance.tab.summary") },
    { value: "approvals" as const, label: t("attendance.tab.approvals") },
  ], [t]);

  return (
    <>
      <PageHeader
        title={t("attendance.title")}
        description={t("attendance.description")}
        breadcrumbs={[{ label: t("attendance.breadcrumb") }]}
        actions={<Link to="/workforce?tab=headcount" className="text-sm font-semibold text-navy-700 hover:underline">{t("attendance.headcountLink")}</Link>}
      />
      <div className="mb-4 overflow-x-auto">
        <Tabs tabs={tabs} value={tab} onChange={setTab} />
      </div>
      {tab === "daily" ? <DailyTab site={site} /> : tab === "summary" ? <SummaryTab site={site} /> : <ApprovalsTab site={site} />}
    </>
  );
}
