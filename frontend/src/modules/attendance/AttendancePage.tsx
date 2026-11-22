import { useMemo, useState } from "react";
import { useSearchParams } from "react-router";
import { useT } from "@/i18n";
import { PageHeader, Tabs } from "@/components/ui";
import { useAuth } from "@/auth/AuthProvider";
import { useLookups } from "@/hooks/useLookups";
import { DailyTab } from "./DailyTab";
import { SummaryTab } from "./SummaryTab";

const SCOPED = ["SUPERVISOR", "LEAD_OPERATOR", "OPERATOR"];

/** Shared site state for both tabs: plant-scoped users are pinned to their own site. */
export function useSiteSelection() {
  const { user } = useAuth();
  const { data: lookups } = useLookups();
  const pinned = user?.plant && SCOPED.includes(user.role) ? user.plant.id : null;
  const plants = lookups?.plants ?? [];
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

type TabKey = "daily" | "summary";

export function AttendancePage() {
  const { t } = useT();
  const site = useSiteSelection();
  const [tab, setTab] = useState<TabKey>("daily");
  const tabs = useMemo(() => [{ value: "daily" as const, label: t("attendance.tab.daily") }, { value: "summary" as const, label: t("attendance.tab.summary") }], [t]);

  return (
    <>
      <PageHeader
        title={t("attendance.title")}
        description={t("attendance.description")}
        breadcrumbs={[{ label: t("attendance.breadcrumb") }]}
      />
      <div className="mb-4 overflow-x-auto">
        <Tabs tabs={tabs} value={tab} onChange={setTab} />
      </div>
      {tab === "daily" ? <DailyTab site={site} /> : <SummaryTab site={site} />}
    </>
  );
}
