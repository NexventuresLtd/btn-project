import { useState } from "react";
import { useSearchParams } from "react-router";
import { ArrowDownToLine, ArrowUpDown, ArrowUpFromLine, Check, Clock, FileDown, Lock, Pencil, Plus, Search, Wallet, X } from "lucide-react";
import {
  Badge, Button, Card, CardHeader, ConfirmDialog, DataTable, ErrorState, Field, FilterBar, Input, Modal, PageHeader, Pagination, PdfButton,
  StatCard, StatusBadge, Tabs, Textarea, useFilters, type Column,
} from "@/components/ui";
import { useT } from "@/i18n";
import { useAuth } from "@/auth/AuthProvider";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { openPdf, qs } from "@/lib/api";
import { formatDate, formatDateTime } from "@/lib/dates";
import { formatInt, formatRWF } from "@/lib/format";
import { cn } from "@/lib/cn";
import { approveEntry, rejectEntry, useEntries, useSummary, PC_KEY, type EntryType, type PettyEntry } from "./api";
import { categoryLabel } from "./labels";
import { EntryFormModal } from "./EntryFormModal";
import { CategoryChart, PlantChart } from "./SpendingCharts";

type StatusTab = "ALL" | "SUBMITTED" | "APPROVED" | "REJECTED";
const PAGE_SIZE = 50;

export function PettyCashPage() {
  const { t, tx } = useT();
  const { user, can } = useAuth();
  const { filters, set, pinnedPlant, query: rangeQuery } = useFilters();
  const [params, setParams] = useSearchParams();
  const status = (params.get("status") as StatusTab | null) ?? "ALL";
  const page = Math.max(1, Number(params.get("page")) || 1);
  const order = params.get("order") === "desc" ? "desc" : "asc";
  const [search, setSearch] = useState(params.get("q") ?? "");
  const q = params.get("q") ?? "";

  const setParam = (patch: Record<string, string | undefined>, keepPage = false) => {
    const next = new URLSearchParams(params);
    for (const [k, v] of Object.entries(patch)) {
      if (v) next.set(k, v);
      else next.delete(k);
    }
    if (!keepPage) next.delete("page");
    setParams(next, { replace: true });
  };

  const summary = useSummary(rangeQuery);
  const entriesQuery = qs({ ...filters, status: status === "ALL" ? undefined : status, q: q || undefined, order, page, pageSize: PAGE_SIZE });
  const entries = useEntries(entriesQuery);

  const [form, setForm] = useState<{ type: EntryType; entry?: PettyEntry } | null>(null);
  const [approving, setApproving] = useState<PettyEntry | null>(null);
  const [rejecting, setRejecting] = useState<PettyEntry | null>(null);

  const canRecord = can("pettycash.record");
  const canApprove = can("pettycash.approve");
  const isOwn = (e: PettyEntry) => e.createdBy.id === user?.id;
  const canEdit = (e: PettyEntry) => canRecord && isOwn(e) && !e.readOnly && (e.status === "SUBMITTED" || e.status === "REJECTED");
  const canReview = (e: PettyEntry) => canApprove && !isOwn(e) && e.status === "SUBMITTED";

  const s = summary.data;
  const counts = entries.data?.statusCounts ?? {};

  const columns: Column<PettyEntry>[] = [
    {
      key: "date",
      header: t("common.date"),
      cell: (e) => (
        <div className="whitespace-nowrap">
          <p className="font-medium text-slate-800">{formatDate(e.date)}</p>
          <p className="text-xs text-slate-500">{e.number}</p>
        </div>
      ),
    },
    {
      key: "desc",
      header: t("common.description"),
      className: "min-w-[200px]",
      cell: (e) => (
        <div>
          <p className="text-slate-800">{e.description}</p>
          <div className="mt-1 flex flex-wrap items-center gap-1.5">
            <Badge tone={e.type === "DEPOSIT" ? "green" : "slate"}>{categoryLabel(tx, e.category)}</Badge>
            {e.plant ? <Badge tone="navy">{e.plant.code}</Badge> : null}
            {e.readOnly ? <Badge tone="purple"><Lock className="size-3" />{t("pettycash.badge.payroll")}</Badge> : null}
            {e.invoiceRef ? <span className="text-xs text-slate-500">{t("pettycash.invoiceShort", { ref: e.invoiceRef })}</span> : null}
          </div>
          <p className="mt-1 text-xs text-slate-500 md:hidden">{[e.payeeName, e.momoNumber].filter(Boolean).join(" · ")}</p>
        </div>
      ),
    },
    {
      key: "payee",
      header: t("pettycash.col.nameMomo"),
      hideOnMobile: true,
      cell: (e) => (
        <div className="whitespace-nowrap">
          <p className="text-slate-800">{e.payeeName ?? (e.type === "DEPOSIT" ? "BTN Manufacturing" : "—")}</p>
          <p className="tabular text-xs text-slate-500">{e.momoNumber ?? ""}</p>
        </div>
      ),
    },
    {
      key: "out",
      header: t("pettycash.col.moneyOut"),
      align: "right",
      cell: (e) =>
        e.type === "EXPENSE" ? (
          <div className="whitespace-nowrap">
            <p className="font-semibold text-slate-900">{formatInt(e.amount)}</p>
            {e.momoCharge ? <p className="text-xs text-slate-500">{t("pettycash.chargeNote", { amount: formatInt(e.momoCharge) })}</p> : null}
          </div>
        ) : null,
    },
    { key: "in", header: t("pettycash.col.deposit"), align: "right", cell: (e) => (e.type === "DEPOSIT" ? <span className="font-semibold text-emerald-700">{formatInt(e.amount)}</span> : null) },
    {
      key: "bal",
      header: t("pettycash.col.balance"),
      align: "right",
      hideOnMobile: true,
      cell: (e) => (e.balanceAfter != null ? <span className={cn("font-medium", e.balanceAfter < 0 ? "text-red-700" : "text-slate-900")}>{formatInt(e.balanceAfter)}</span> : <span className="text-slate-300">—</span>),
    },
    {
      key: "status",
      header: t("common.status"),
      cell: (e) => (
        <div className="max-w-[200px]">
          <StatusBadge status={e.status} />
          {e.status === "APPROVED" ? (
            <p className="mt-1 text-xs text-slate-500" title={e.reviewedAt ? formatDateTime(e.reviewedAt) : undefined}>{e.autoApproved ? t("pettycash.autoBelowLimit") : t("pettycash.approvedBy", { name: e.reviewedBy?.name ?? "—" })}</p>
          ) : e.status === "REJECTED" && e.reviewComment ? (
            <p className="mt-1 line-clamp-2 text-xs text-red-700" title={e.reviewComment}>“{e.reviewComment}”</p>
          ) : e.status === "SUBMITTED" && isOwn(e) && canApprove ? (
            <p className="mt-1 text-xs text-slate-500">{t("pettycash.needsOtherApprover")}</p>
          ) : null}
        </div>
      ),
    },
    {
      key: "actions",
      header: <span className="sr-only">{t("common.actions")}</span>,
      align: "right",
      cell: (e) => (
        <div className="flex items-center justify-end gap-1.5">
          {canReview(e) ? (
            <>
              <Button size="sm" variant="success" icon={<Check className="size-3.5" />} onClick={() => setApproving(e)}>{t("common.approve")}</Button>
              <Button size="sm" variant="secondary" icon={<X className="size-3.5" />} onClick={() => setRejecting(e)} className="text-red-700">{t("common.reject")}</Button>
            </>
          ) : null}
          {canEdit(e) ? (
            <Button size="icon" variant="ghost" title={e.status === "REJECTED" ? t("pettycash.correctResubmit") : t("common.edit")} aria-label={t("pettycash.editEntry", { number: e.number })} onClick={() => setForm({ type: e.type, entry: e })}>
              <Pencil className="size-4" />
            </Button>
          ) : null}
          <Button size="icon" variant="ghost" title={t("pettycash.voucherPdf")} aria-label={t("pettycash.voucherPdfFor", { number: e.number })} onClick={() => openPdf(`/pettycash/pdf/voucher/${e.id}`)}>
            <FileDown className="size-4" />
          </Button>
        </div>
      ),
    },
  ];

  const tabs: { value: StatusTab; label: string; count?: number }[] = [
    { value: "ALL", label: t("common.all"), count: counts.ALL },
    { value: "SUBMITTED", label: t("enums.status.SUBMITTED"), count: counts.SUBMITTED ?? 0 },
    { value: "APPROVED", label: t("enums.status.APPROVED"), count: counts.APPROVED ?? 0 },
    { value: "REJECTED", label: t("enums.status.REJECTED"), count: counts.REJECTED ?? 0 },
  ];

  const kpi = (v: number | undefined) => (summary.isLoading ? "…" : v == null ? "—" : formatRWF(v));

  return (
    <>
      <PageHeader
        title={t("pettycash.page.title")}
        description={t("pettycash.page.description")}
        actions={
          <>
            <PdfButton path="/pettycash/pdf/statement" params={{ from: filters.from, to: filters.to, plantId: filters.plantId || undefined }} label={t("pettycash.page.statementPdf")} />
            {canRecord ? (
              <>
                <Button variant="secondary" icon={<ArrowDownToLine className="size-4" />} onClick={() => setForm({ type: "DEPOSIT" })}>{t("pettycash.form.recordDeposit")}</Button>
                <Button icon={<Plus className="size-4" />} onClick={() => setForm({ type: "EXPENSE" })}>{t("pettycash.form.recordExpense")}</Button>
              </>
            ) : null}
          </>
        }
      />

      <FilterBar filters={filters} onChange={set} pinnedPlant={pinnedPlant} className="mb-5" />

      {summary.isError ? (
        <Card className="mb-5"><ErrorState error={summary.error} retry={() => summary.refetch()} /></Card>
      ) : (
        <div className="mb-5 grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
          <StatCard label={t("pettycash.kpi.currentBalance")} value={kpi(s?.currentBalance)} icon={<Wallet />} tone={s && s.currentBalance < 0 ? "red" : "navy"} hint={t("pettycash.kpi.currentBalanceHint")} />
          <StatCard label={t("pettycash.kpi.deposits")} value={kpi(s?.deposits)} icon={<ArrowDownToLine />} tone="green" hint={s ? t("pettycash.kpi.depositsHint", { topups: t("pettycash.topups", { count: s.depositCount }), opening: formatRWF(s.opening) }) : undefined} />
          <StatCard label={t("pettycash.kpi.expenses")} value={kpi(s?.expensesTotal)} icon={<ArrowUpFromLine />} tone="amber" hint={s ? t("pettycash.kpi.expensesHint", { payments: t("pettycash.payments", { count: s.expenseCount }), charges: formatRWF(s.charges) }) : undefined} />
          <StatCard
            label={t("pettycash.kpi.pending")}
            value={summary.isLoading ? "…" : formatInt(s?.pending.count ?? 0)}
            icon={<Clock />}
            tone={s?.pending.count ? "red" : "blue"}
            hint={s ? t("pettycash.kpi.pendingHint", { amount: formatRWF(s.pending.expenseAmount + s.pending.depositAmount) }) : undefined}
          />
        </div>
      )}

      <div className="mb-5 grid gap-5 lg:grid-cols-5">
        <CategoryChart data={s} loading={summary.isLoading} error={summary.error} retry={() => summary.refetch()} />
        <PlantChart data={s} loading={summary.isLoading} error={summary.error} retry={() => summary.refetch()} />
      </div>

      <Card>
        <CardHeader
          title={t("pettycash.ledger.title")}
          description={
            entries.data
              ? t("pettycash.ledger.desc", { opening: formatRWF(entries.data.openingBalance), from: formatDate(entries.data.from), closing: formatRWF(entries.data.closingBalance), to: formatDate(entries.data.to) }) + (filters.plantId ? t("pettycash.ledger.allPlantsNote") : "")
              : t("pettycash.ledger.running")
          }
        />
        <div className="flex flex-wrap items-center justify-between gap-3 px-5 pb-4">
          <Tabs tabs={tabs} value={status} onChange={(v) => setParam({ status: v === "ALL" ? undefined : v })} className="max-w-full overflow-x-auto" />
          <div className="flex w-full items-center gap-2 sm:w-auto">
            <form
              className="relative min-w-0 flex-1 sm:flex-none"
              onSubmit={(ev) => {
                ev.preventDefault();
                setParam({ q: search.trim() || undefined });
              }}
            >
              <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-slate-400" />
              <Input
                type="search"
                value={search}
                onChange={(ev) => {
                  setSearch(ev.target.value);
                  if (!ev.target.value) setParam({ q: undefined });
                }}
                placeholder={t("pettycash.search.placeholder")}
                className="h-9 w-full pl-9 sm:w-56"
                aria-label={t("pettycash.search.aria")}
              />
            </form>
            <Button size="sm" variant="secondary" icon={<ArrowUpDown className="size-3.5" />} onClick={() => setParam({ order: order === "asc" ? "desc" : undefined })} title={t("pettycash.sort.title")}>
              {order === "asc" ? t("pettycash.sort.oldest") : t("pettycash.sort.newest")}
            </Button>
          </div>
        </div>
        {entries.isError ? (
          <ErrorState error={entries.error} retry={() => entries.refetch()} />
        ) : (
          <>
            <DataTable
              columns={columns}
              rows={entries.data?.items}
              rowKey={(e) => e.id}
              loading={entries.isLoading}
              empty={
                q || status !== "ALL"
                  ? t("pettycash.empty.filtered")
                  : canRecord
                    ? t("pettycash.empty.canRecord")
                    : t("pettycash.empty.none")
              }
              className={cn(entries.isFetching && !entries.isLoading && "opacity-70 transition-opacity")}
            />
            {entries.data ? <Pagination page={entries.data.page} pageSize={entries.data.pageSize} total={entries.data.total} onPage={(p) => setParam({ page: String(p) }, true)} /> : null}
          </>
        )}
      </Card>

      <EntryFormModal
        open={!!form}
        onClose={() => setForm(null)}
        type={form?.type ?? "EXPENSE"}
        entry={form?.entry}
        currentBalance={s?.currentBalance}
        approvalLimit={s?.approvalLimit}
      />
      <ApproveDialog entry={approving} balance={s?.currentBalance} onClose={() => setApproving(null)} />
      <RejectDialog entry={rejecting} onClose={() => setRejecting(null)} />
    </>
  );
}

function EntrySummary({ e }: { e: PettyEntry }) {
  const { t, tx } = useT();
  return (
    <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 rounded-xl border border-line bg-slate-50/60 px-4 py-3 text-sm">
      <dt className="text-slate-500">{t("pettycash.summary.entry")}</dt>
      <dd className="font-medium text-slate-900">{e.number} · {formatDate(e.date)}</dd>
      <dt className="text-slate-500">{e.type === "DEPOSIT" ? t("pettycash.summary.deposit") : t("pettycash.summary.paidTo")}</dt>
      <dd className="text-slate-900">{e.type === "DEPOSIT" ? t("pettycash.summary.floatTopup") : `${e.payeeName ?? "—"}${e.momoNumber ? ` · ${e.momoNumber}` : ""}`}</dd>
      <dt className="text-slate-500">{t("pettycash.summary.for")}</dt>
      <dd className="text-slate-900">{e.description} <span className="text-slate-500">({categoryLabel(tx, e.category)}{e.plant ? `, ${e.plant.code}` : ""})</span></dd>
      <dt className="text-slate-500">{t("common.amount")}</dt>
      <dd className="tabular font-semibold text-slate-900">
        {formatRWF(e.amount)}
        {e.momoCharge ? <span className="font-normal text-slate-500"> {t("pettycash.summary.chargeTotal", { charge: formatInt(e.momoCharge), total: formatRWF(e.total) })}</span> : null}
      </dd>
      <dt className="text-slate-500">{t("pettycash.summary.recordedBy")}</dt>
      <dd className="text-slate-900">{e.createdBy.name} · {formatDateTime(e.createdAt)}</dd>
    </dl>
  );
}

function ApproveDialog({ entry, balance, onClose }: { entry: PettyEntry | null; balance: number | undefined; onClose: () => void }) {
  const { t } = useT();
  const [comment, setComment] = useState("");
  const approve = useMutate(approveEntry, {
    success: (d) => t("pettycash.approve.done", { number: d.number }),
    invalidate: [PC_KEY, ["dashboard"]],
    onSuccess: () => {
      setComment("");
      onClose();
    },
  });
  const short = entry?.type === "EXPENSE" && balance != null && entry.total > balance;
  return (
    <ConfirmDialog
      open={!!entry}
      onClose={() => {
        approve.reset();
        onClose();
      }}
      onConfirm={() => entry && approve.mutate({ id: entry.id, comment: comment.trim() || undefined })}
      title={entry?.type === "DEPOSIT" ? t("pettycash.approve.titleDeposit") : t("pettycash.approve.titleExpense")}
      confirmLabel={t("common.approve")}
      tone="success"
      loading={approve.isPending}
    >
      {entry ? (
        <div className="space-y-3">
          <EntrySummary e={entry} />
          {entry.type === "EXPENSE" && balance != null ? (
            <p className={cn("text-sm", short ? "font-medium text-red-700" : "text-slate-600")}>
              {t("pettycash.approve.balanceAfter", { from: formatRWF(balance), to: formatRWF(balance - entry.total) })}
              {short ? t("pettycash.approve.notEnough") : ""}
            </p>
          ) : null}
          <Field label={t("pettycash.approve.comment")}>
            <Input value={comment} maxLength={500} onChange={(ev) => setComment(ev.target.value)} />
          </Field>
        </div>
      ) : null}
    </ConfirmDialog>
  );
}

function RejectDialog({ entry, onClose }: { entry: PettyEntry | null; onClose: () => void }) {
  const { t } = useT();
  const [comment, setComment] = useState("");
  const [touched, setTouched] = useState(false);
  const reject = useMutate(rejectEntry, {
    success: (d) => t("pettycash.reject.done", { number: d.number, name: d.createdBy.name }),
    invalidate: [PC_KEY, ["dashboard"]],
    onSuccess: () => close(),
  });
  function close() {
    setComment("");
    setTouched(false);
    reject.reset();
    onClose();
  }
  const localError = touched && comment.trim().length < 3 ? t("pettycash.reject.reasonRequired") : undefined;
  return (
    <Modal
      open={!!entry}
      onClose={close}
      size="sm"
      title={t("pettycash.reject.title")}
      description={t("pettycash.reject.desc")}
      footer={
        <>
          <Button variant="secondary" onClick={close}>{t("common.cancel")}</Button>
          <Button
            variant="danger"
            loading={reject.isPending}
            onClick={() => {
              setTouched(true);
              if (entry && comment.trim().length >= 3) reject.mutate({ id: entry.id, comment: comment.trim() });
            }}
          >
            {t("common.reject")}
          </Button>
        </>
      }
    >
      {entry ? (
        <div className="space-y-4">
          <EntrySummary e={entry} />
          <Field label={t("pettycash.reject.reason")} required error={localError ?? fieldError(reject.error, "comment")}>
            <Textarea value={comment} rows={3} maxLength={500} autoFocus onChange={(ev) => setComment(ev.target.value)} invalid={!!localError} placeholder={t("pettycash.reject.reasonPlaceholder")} />
          </Field>
        </div>
      ) : null}
    </Modal>
  );
}

