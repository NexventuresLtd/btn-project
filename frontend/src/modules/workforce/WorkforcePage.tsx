import { useMemo } from "react";
import { useSearchParams } from "react-router";
import { EmptyState, PageHeader, Tabs } from "@/components/ui";
import { useAuth } from "@/auth/AuthProvider";
import { useT, type TKey } from "@/i18n";
import { ReportsTab } from "./ReportsTab";
import { HeadcountTab } from "./HeadcountTab";
import { RateRulesTab } from "./RateRulesTab";
import { PieceRatesTab } from "./PieceRatesTab";
import { PieceWorkTab } from "./PieceWorkTab";
import { PaymentsTab } from "./PaymentsTab";

type TabKey = "reports" | "headcount" | "rules" | "pieceRates" | "pieceWork" | "payments";

const RATE_VIEW = ["payroll.view", "wages.view", "workers.manage", "masterdata.manage", "settings.manage"];

/** Each tab and the permissions (any of) that open it — the API enforces the same rules. */
const TABS: { key: TabKey; label: TKey; perms: string[] }[] = [
  { key: "reports", label: "workforce.tab.reports", perms: ["wages.view", "payroll.view"] },
  { key: "headcount", label: "workforce.tab.headcount", perms: ["attendance.view"] },
  { key: "payments", label: "workforce.tab.payments", perms: ["wages.view"] },
  { key: "rules", label: "workforce.tab.rules", perms: RATE_VIEW },
  { key: "pieceRates", label: "workforce.tab.pieceRates", perms: [...RATE_VIEW, "attendance.view"] },
  { key: "pieceWork", label: "workforce.tab.pieceWork", perms: ["attendance.view"] },
];

export function WorkforcePage() {
  const { t } = useT();
  const { can } = useAuth();
  const [params, setParams] = useSearchParams();
  const tabs = useMemo(() => TABS.filter((x) => x.perms.some(can)), [can]);
  const requested = params.get("tab") as TabKey | null;
  const tab = tabs.find((x) => x.key === requested)?.key ?? tabs[0]?.key;
  const setTab = (k: TabKey) => {
    // keep the tab only: filters of one tab mean something else on another
    setParams(new URLSearchParams({ tab: k }), { replace: true });
  };

  return (
    <>
      <PageHeader title={t("workforce.title")} description={t("workforce.subtitle")} breadcrumbs={[{ label: t("workforce.nav") }]} />
      {!tab ? (
        <EmptyState title={t("workforce.noAccess")} />
      ) : (
        <>
          <div className="mb-4 overflow-x-auto">
            <Tabs tabs={tabs.map((x) => ({ value: x.key, label: t(x.label) }))} value={tab} onChange={setTab} />
          </div>
          {tab === "reports" ? <ReportsTab /> : null}
          {tab === "headcount" ? <HeadcountTab /> : null}
          {tab === "payments" ? <PaymentsTab /> : null}
          {tab === "rules" ? <RateRulesTab /> : null}
          {tab === "pieceRates" ? <PieceRatesTab /> : null}
          {tab === "pieceWork" ? <PieceWorkTab /> : null}
        </>
      )}
    </>
  );
}
