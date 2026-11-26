import { useEffect, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { ShieldCheck, UserSearch } from "lucide-react";
import { Badge, Card, CardHeader, DataTable, EmptyState, ErrorState, PageHeader, Pagination, Select, Tabs, type Column } from "@/components/ui";
import { useT } from "@/i18n";
import { cn } from "@/lib/cn";
import { SearchInput, useDebounced } from "@/modules/admin/components";
import { useSubjects, type SubjectHit } from "./api";
import { AccessLogTable } from "./components";

type Tab = "subjects" | "log";

/** NFR-D2 data-subject tools: find a worker or customer, then open their page (export, PDF, correct, access log). */
export function PrivacyPage() {
  const { t } = useT();
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const tab: Tab = params.get("tab") === "log" ? "log" : "subjects";
  const type = params.get("type") ?? "";
  const page = Math.max(1, Number(params.get("page")) || 1);
  const [search, setSearch] = useState(params.get("q") ?? "");
  const q = useDebounced(search.trim());
  const update = (patch: Record<string, string | undefined>) => {
    const n = new URLSearchParams(params);
    for (const [k, v] of Object.entries(patch)) {
      if (v) n.set(k, v);
      else n.delete(k);
    }
    setParams(n, { replace: true });
  };
  useEffect(() => {
    if (q !== (params.get("q") ?? "")) update({ q: q || undefined, page: undefined });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [q]);

  const subjects = useSubjects({ type, q: params.get("q") ?? "", page });
  const open = (s: SubjectHit) => navigate(`/admin/privacy/${s.type}/${encodeURIComponent(s.id)}`);

  const columns: Column<SubjectHit>[] = [
    {
      key: "name",
      header: t("privacy.col.name"),
      cell: (s) => (
        <Link to={`/admin/privacy/${s.type}/${encodeURIComponent(s.id)}`} onClick={(e) => e.stopPropagation()} className="font-medium text-navy-800 hover:underline focus-visible:underline">
          {s.name}
        </Link>
      ),
    },
    { key: "type", header: t("privacy.col.type"), cell: (s) => <Badge tone={s.type === "worker" ? "navy" : "blue"}>{t(`privacy.type.${s.type}`)}</Badge> },
    { key: "identity", header: t("privacy.col.identity"), cell: (s) => <span className="font-mono text-xs text-slate-700">{s.identity ?? "—"}</span>, hideOnMobile: true },
    { key: "phone", header: t("privacy.col.phone"), cell: (s) => <span className="text-slate-700 tabular">{s.phone ?? s.email ?? "—"}</span>, hideOnMobile: true },
    { key: "site", header: t("privacy.col.site"), cell: (s) => s.site ?? "—", hideOnMobile: true },
    { key: "status", header: t("privacy.col.status"), cell: (s) => <Badge tone={s.isActive ? "green" : "slate"} dot>{s.isActive ? t("privacy.status.active") : t("privacy.status.inactive")}</Badge> },
  ];

  return (
    <>
      <PageHeader
        title={t("privacy.title")}
        description={t("privacy.description")}
        breadcrumbs={[{ label: t("privacy.breadcrumb") }, { label: t("privacy.nav") }]}
      />
      <p className="-mt-2 mb-4 flex items-center gap-1.5 text-xs text-slate-600">
        <ShieldCheck className="size-4 shrink-0 text-navy-600" aria-hidden />
        {t("privacy.legal")}
      </p>
      <Tabs<Tab>
        className="mb-4"
        value={tab}
        onChange={(v) => update({ tab: v === "log" ? "log" : undefined, page: undefined })}
        tabs={[{ value: "subjects", label: t("privacy.tabs.subjects") }, { value: "log", label: t("privacy.tabs.accessLog") }]}
      />

      {tab === "subjects" ? (
        <Card>
          <div className="flex flex-wrap items-center gap-2 px-5 py-4">
            <Select value={type} onChange={(e) => update({ type: e.target.value || undefined, page: undefined })} className="w-full sm:w-56" aria-label={t("privacy.search.type")}>
              <option value="">{t("privacy.type.all")}</option>
              <option value="worker">{t("privacy.type.workers")}</option>
              <option value="customer">{t("privacy.type.customers")}</option>
            </Select>
            <SearchInput value={search} onChange={setSearch} placeholder={t("privacy.search.placeholder")} className="w-full sm:w-96" />
          </div>
          {subjects.isError ? (
            <ErrorState error={subjects.error} retry={() => subjects.refetch()} />
          ) : !subjects.isLoading && !subjects.data?.items.length ? (
            <div className="border-t border-line"><EmptyState icon={<UserSearch />} title={t("privacy.empty.searchTitle")} description={t("privacy.empty.searchText")} /></div>
          ) : (
            <>
              <DataTable
                className={cn("transition-opacity", subjects.isFetching && !subjects.isLoading && "opacity-60")}
                columns={columns}
                rows={subjects.data?.items}
                rowKey={(s) => `${s.type}:${s.id}`}
                loading={subjects.isLoading}
                onRowClick={open}
              />
              {subjects.data ? (
                <Pagination page={subjects.data.page} pageSize={subjects.data.pageSize} total={subjects.data.total} onPage={(p) => update({ page: String(p) })} />
              ) : null}
            </>
          )}
        </Card>
      ) : (
        <Card>
          <CardHeader title={t("privacy.accessLog.all")} />
          <AccessLogTable showSubject />
        </Card>
      )}
    </>
  );
}
