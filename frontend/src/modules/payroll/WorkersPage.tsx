import { useEffect, useState } from "react";
import { Link, useSearchParams } from "react-router";
import { BadgeCheck, Clock, History, Pencil, Search, TriangleAlert, UserPlus } from "lucide-react";
import { Alert, Badge, Button, Card, DataTable, ErrorState, FilterBar, Input, PageHeader, Pagination, Select, Tabs, useFilters, type Column, ExportMenu } from "@/components/ui";
import { useT } from "@/i18n";
import { useAuth } from "@/auth/AuthProvider";
import { formatInt } from "@/lib/format";
import { formatDate } from "@/lib/dates";
import { useMutate } from "@/hooks/useMutate";
import { approveWorker, EMPLOYMENT_STATUSES, PAYROLL_KEY, RATE_BASES, useWorkers, type EmploymentStatus, type Worker } from "./api";
import { WorkerModal } from "./WorkerModal";

type ActiveFilter = "true" | "false" | "all";
const ACTIVE_VALUES: ActiveFilter[] = ["true", "false", "all"];

export function MismatchBadge() {
  const { t } = useT();
  return (
    <Badge tone="amber" className="gap-1">
      <TriangleAlert className="size-3" /> {t("payroll.mismatchBadge")}
    </Badge>
  );
}

const STATUS_TONE: Record<EmploymentStatus, "green" | "slate" | "red"> = { ACTIVE: "green", INACTIVE: "slate", LEFT: "red" };

/** WRK-01 employment status (active / inactive / left). */
export function EmploymentBadge({ status, leftDate }: { status: EmploymentStatus; leftDate?: string | null }) {
  const { tx, t } = useT();
  return (
    <Badge tone={STATUS_TONE[status] ?? "slate"} dot>
      {tx(`payroll.wrk.status.${status}`)}{status === "LEFT" && leftDate ? ` · ${t("payroll.wrk.leftOn", { date: formatDate(leftDate) })}` : ""}
    </Badge>
  );
}

/** WRK-01: a supervisor's registration waiting for Finance approval. */
export function PendingRegistrationBadge() {
  const { t } = useT();
  return (
    <Badge tone="amber" className="gap-1">
      <Clock className="size-3" /> {t("payroll.reg.pending")}
    </Badge>
  );
}

export function WorkersPage() {
  const { t, tx } = useT();
  const { can, user } = useAuth();
  const canManage = can("workers.manage");
  // a site supervisor registers new casual workers at his site (pending Finance approval) and corrects them until approved
  const canRegister = canManage || can("workers.register");
  const editable = (w: Worker) => canManage || (w.registrationStatus === "PENDING" && w.registeredById === user?.id);
  const approve = useMutate((w: Worker) => approveWorker(w.id), {
    success: (w) => t("payroll.reg.approved", { name: w.fullName }),
    invalidate: [[...PAYROLL_KEY, "workers"]],
  });
  const canWages = can("wages.view");
  const { filters, set, pinnedPlant } = useFilters();
  const [params, setParams] = useSearchParams();
  const active = (ACTIVE_VALUES.find((v) => v === params.get("active")) ?? "true") as ActiveFilter;
  const page = Math.max(1, Number(params.get("page")) || 1);
  const status = EMPLOYMENT_STATUSES.find((s) => s === params.get("status"));
  const rateBasis = RATE_BASES.find((b) => b === params.get("rateBasis"));
  const registration = params.get("registration") === "PENDING" ? ("PENDING" as const) : undefined;
  const [search, setSearch] = useState(params.get("q") ?? "");
  const q = params.get("q") ?? "";
  const [editing, setEditing] = useState<Worker | null>(null);
  const [modalOpen, setModalOpen] = useState(false);

  const setParam = (key: string, value: string | null) => {
    const next = new URLSearchParams(params);
    if (value) next.set(key, value);
    else next.delete(key);
    if (key !== "page") next.delete("page");
    setParams(next, { replace: true });
  };

  // debounce typing into the URL
  useEffect(() => {
    const id = setTimeout(() => {
      if (search.trim() !== q) setParam("q", search.trim() || null);
    }, 300);
    return () => clearTimeout(id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [search]);

  // an explicit employment status filter replaces the active/inactive tabs
  const workers = useWorkers({ plantId: filters.plantId || undefined, q: q || undefined, active: status ? "all" : active, status, rateBasis, registration, page, pageSize: 50 });
  const data = workers.data;

  const openNew = () => {
    setEditing(null);
    setModalOpen(true);
  };
  const openEdit = (w: Worker) => {
    setEditing(w);
    setModalOpen(true);
  };

  const columns: Column<Worker>[] = [
    {
      key: "name",
      header: t("payroll.col.worker"),
      cell: (w) => (
        <div className="min-w-0">
          <p className="font-semibold text-slate-900">{w.fullName}</p>
          <p className="tabular text-xs text-slate-500">{w.nationalId ?? t("payroll.col.noId")}</p>
        </div>
      ),
    },
    { key: "plant", header: t("common.plant"), cell: (w) => <span className="whitespace-nowrap">{w.plant.code}</span> },
    { key: "job", header: t("payroll.col.jobTitle"), hideOnMobile: true, cell: (w) => w.jobTitle },
    { key: "basis", header: t("payroll.wrk.rateBasis"), hideOnMobile: true, cell: (w) => <Badge tone={w.rateBasis === "DAILY" ? "navy" : w.rateBasis === "PIECE" ? "purple" : "blue"}>{tx(`payroll.wrk.basis.${w.rateBasis ?? "DAILY"}`)}</Badge> },
    { key: "wage", header: t("payroll.col.dailyWage"), align: "right", cell: (w) => (w.rateBasis === "PIECE" && !w.dailyWage ? <span className="text-slate-400">—</span> : formatInt(w.dailyWage)) },
    { key: "phone", header: t("payroll.wrk.phone"), hideOnMobile: true, cell: (w) => <span className="tabular whitespace-nowrap">{w.phone ?? "—"}</span> },
    { key: "momo", header: t("payroll.col.momoNumber"), hideOnMobile: true, cell: (w) => <span className="tabular whitespace-nowrap">{w.momoNumber ?? "—"}</span> },
    {
      key: "momoName",
      header: t("payroll.col.momoName"),
      hideOnMobile: true,
      cell: (w) => (
        <div className="flex flex-wrap items-center gap-1.5">
          <span className={w.momoName ? "" : "text-slate-400"}>{w.momoName ?? t("payroll.col.notRecorded")}</span>
          {w.momoNameMismatch ? <MismatchBadge /> : null}
        </div>
      ),
    },
    {
      key: "status",
      header: t("common.status"),
      cell: (w) => (
        <div className="flex flex-wrap items-center gap-1.5">
          <EmploymentBadge status={w.employmentStatus ?? (w.isActive ? "ACTIVE" : "INACTIVE")} leftDate={w.leftDate} />
          {w.registrationStatus === "PENDING" ? <PendingRegistrationBadge /> : null}
        </div>
      ),
    },
    ...(canWages
      ? [
          {
            key: "history",
            header: <span className="sr-only">{t("payroll.wrk.history")}</span>,
            align: "right" as const,
            cell: (w: Worker) => (
              <Link to={`/workforce/workers/${w.id}`} onClick={(e) => e.stopPropagation()} className="inline-flex items-center gap-1 rounded-lg px-2 py-1.5 text-xs font-semibold whitespace-nowrap text-navy-700 hover:bg-navy-50" aria-label={t("payroll.wrk.historyAria", { name: w.fullName })}>
                <History className="size-3.5" /> {t("payroll.wrk.history")}
              </Link>
            ),
          },
        ]
      : []),
    ...(canRegister
      ? [
          {
            key: "edit",
            header: <span className="sr-only">{t("common.edit")}</span>,
            align: "right" as const,
            cell: (w: Worker) => (
              <div className="flex justify-end gap-1">
                {canManage && w.registrationStatus === "PENDING" && w.registeredById !== user?.id ? (
                  <Button onlineOnly variant="success" size="sm" icon={<BadgeCheck className="size-3.5" />} loading={approve.isPending && approve.variables?.id === w.id}
                    onClick={(e) => { e.stopPropagation(); approve.mutate(w); }} aria-label={t("payroll.reg.approveAria", { name: w.fullName })}>
                    {t("payroll.reg.approve")}
                  </Button>
                ) : null}
                {editable(w) ? (
                  <Button variant="ghost" size="sm" icon={<Pencil className="size-3.5" />} onClick={(e) => { e.stopPropagation(); openEdit(w); }} aria-label={t("payroll.col.editAria", { name: w.fullName })}>
                    {t("common.edit")}
                  </Button>
                ) : null}
              </div>
            ),
          },
        ]
      : []),
  ];

  return (
    <>
      <PageHeader
        title={t("payroll.workersPage.title")}
        breadcrumbs={[{ label: t("payroll.title"), to: "/payroll" }, { label: t("payroll.workersPage.title") }]}
        description={t("payroll.workersPage.description")}
        actions={<div className="flex flex-wrap items-center gap-2"><ExportMenu path="/payroll/workers" params={{ plantId: filters.plantId || undefined, q: q || undefined, active: status ? "all" : active, status, rateBasis }} />{canRegister ? <Button icon={<UserPlus className="size-4" />} onClick={openNew}>{t("payroll.workersPage.register")}</Button> : null}</div>}
      />

      <FilterBar filters={filters} onChange={set} pinnedPlant={pinnedPlant} showRange={false} className="mb-5">
        <div className="relative w-full sm:w-72">
          <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-slate-400" />
          <Input value={search} onChange={(e) => setSearch(e.target.value)} placeholder={t("payroll.workersPage.searchPlaceholder")} className="pl-9" aria-label={t("payroll.workersPage.searchAria")} />
        </div>
        <Select value={status ?? ""} onChange={(e) => setParam("status", e.target.value || null)} className="h-10 w-full sm:w-44" aria-label={t("payroll.wrk.filterStatus")}>
          <option value="">{t("payroll.wrk.anyStatus")}</option>
          {EMPLOYMENT_STATUSES.map((s) => <option key={s} value={s}>{tx(`payroll.wrk.status.${s}`)}</option>)}
        </Select>
        <Select value={registration ?? ""} onChange={(e) => setParam("registration", e.target.value || null)} className="h-10 w-full sm:w-52" aria-label={t("payroll.reg.filter")}>
          <option value="">{t("payroll.reg.any")}</option>
          <option value="PENDING">{t("payroll.reg.pendingOnly")}</option>
        </Select>
        <Select value={rateBasis ?? ""} onChange={(e) => setParam("rateBasis", e.target.value || null)} className="h-10 w-full sm:w-44" aria-label={t("payroll.wrk.filterBasis")}>
          <option value="">{t("payroll.wrk.anyBasis")}</option>
          {RATE_BASES.map((b) => <option key={b} value={b}>{tx(`payroll.wrk.basis.${b}`)}</option>)}
        </Select>
      </FilterBar>

      {data && data.mismatchCount > 0 ? (
        <div className="mb-5">
          <Alert tone="amber" title={t("payroll.workersPage.mismatchTitle", { count: data.mismatchCount })}>
            {t("payroll.workersPage.mismatchText")}
          </Alert>
        </div>
      ) : null}

      <Card>
        <div className="flex flex-wrap items-center justify-between gap-3 px-5 pt-5 pb-4">
          <Tabs tabs={ACTIVE_VALUES.map((v) => ({ value: v, label: v === "all" ? t("common.all") : v === "true" ? t("enums.status.ACTIVE") : t("enums.status.INACTIVE") }))} value={active} onChange={(v) => setParam("active", v === "true" ? null : v)} />
          {data ? <span className="text-xs text-slate-500">{t("payroll.workersPage.count", { count: data.total })}</span> : null}
        </div>
        {workers.isError ? (
          <ErrorState error={workers.error} retry={() => workers.refetch()} />
        ) : (
          <>
            <DataTable
              columns={columns}
              rows={data?.items}
              rowKey={(w) => w.id}
              loading={workers.isLoading}
              onRowClick={canRegister ? (w) => editable(w) && openEdit(w) : undefined}
              empty={
                <div className="space-y-1">
                  <p className="font-medium text-slate-700">{q ? t("payroll.workersPage.noMatch", { q }) : t("payroll.workersPage.none")}</p>
                  <p>{canRegister ? t("payroll.workersPage.registerHint") : t("payroll.workersPage.tryOther")}</p>
                </div>
              }
            />
            {data ? <Pagination page={data.page} pageSize={data.pageSize} total={data.total} onPage={(p) => setParam("page", String(p))} /> : null}
          </>
        )}
      </Card>

      <WorkerModal open={modalOpen} onClose={() => setModalOpen(false)} worker={editing} pinnedPlant={pinnedPlant} defaultPlant={filters.plantId || undefined} />
    </>
  );
}
