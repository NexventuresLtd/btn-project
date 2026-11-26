import { useMemo, useRef, useState, type DragEvent } from "react";
import { useQueryClient } from "@tanstack/react-query";
import {
  AlertTriangle, Boxes, Building2, CheckCircle2, CircleDashed, Download, FileSpreadsheet, FileUp, History, PackageSearch, PlusCircle, RefreshCw,
  ShoppingBag, Truck, Upload, Users, X, XCircle,
} from "lucide-react";
import { useT } from "@/i18n";
import {
  Alert, Badge, Button, Card, CardHeader, ConfirmDialog, DataTable, EmptyState, ErrorState, PageHeader, Pagination, Spinner, StatCard, Tabs,
  type Column, type Tone,
  radioGroupKeys,
} from "@/components/ui";
import { ApiError } from "@/lib/api";
import { cn } from "@/lib/cn";
import { formatDateTime } from "@/lib/dates";
import { formatInt } from "@/lib/format";
import { useMutate } from "@/hooks/useMutate";
import {
  IMPORT_KEY, commitFile, downloadReport, downloadTemplate, useImportHistory, useImportKinds, validateFile,
  type HistoryRow, type ImportItem, type ImportKind, type ImportReport, type RowAction,
} from "./api";

const KIND_ICONS: Record<string, typeof Boxes> = {
  products: ShoppingBag, materials: PackageSearch, customers: Truck, workers: Users, sites: Building2, "opening-stock": Boxes, "opening-materials": FileSpreadsheet,
};

const ACTION_TONE: Record<RowAction, Tone> = { create: "green", update: "blue", unchanged: "slate", error: "red" };
const MAX_SHOWN = 500;

type Filter = "all" | "errors" | "warnings" | "changes";

const mb = (bytes: number) => `${Math.round(bytes / (1024 * 1024))} MB`;

export function ImportPage() {
  const { t } = useT();
  const qc = useQueryClient();
  const kindsQ = useImportKinds();
  const kinds = kindsQ.data?.items ?? [];
  const limits = kindsQ.data?.limits ?? { maxBytes: 2 * 1024 * 1024, maxRows: 10_000 };
  const [kindName, setKindName] = useState<string | null>(null);
  const kind = kinds.find((k) => k.kind === kindName) ?? kinds[0] ?? null;
  const [file, setFile] = useState<File | null>(null);
  const [fileError, setFileError] = useState<string | null>(null);
  const [report, setReport] = useState<ImportReport | null>(null);
  const [confirm, setConfirm] = useState(false);

  const reset = () => {
    setFile(null);
    setReport(null);
    setFileError(null);
  };

  const check = useMutate(validateFile, {
    invalidate: [[...IMPORT_KEY, "history"]],
    onSuccess: (r) => setReport(r),
  });
  const commit = useMutate(commitFile, {
    success: (r) => t("dataimport.success", { create: r.summary.create, update: r.summary.update, unchanged: r.summary.unchanged }),
    onSuccess: (r) => {
      setReport(r);
      setConfirm(false);
      qc.invalidateQueries(); // master data, stock and worker lists all may have changed
    },
  });

  function chooseKind(k: ImportKind) {
    if (k.kind === kind?.kind) return;
    setKindName(k.kind);
    reset();
  }
  const kindKeys = radioGroupKeys(kinds.map((k) => k.kind), kind?.kind, (v) => { const k = kinds.find((x) => x.kind === v); if (k) chooseKind(k); });

  function pick(f: File | null | undefined) {
    setReport(null);
    setFileError(null);
    if (!f) return;
    if (!/\.(csv|txt)$/i.test(f.name) && !/csv|text\/plain/.test(f.type)) {
      setFile(null);
      setFileError(t("dataimport.drop.notCsv"));
      return;
    }
    if (f.size > limits.maxBytes) {
      setFile(null);
      setFileError(t("dataimport.drop.tooLarge", { size: mb(limits.maxBytes) }));
      return;
    }
    setFile(f);
  }

  const checkError = check.error instanceof ApiError ? check.error.fields?.file ?? check.error.message : null;
  const changes = report ? report.summary.create + report.summary.update : 0;

  return (
    <>
      <PageHeader
        title={t("dataimport.title")}
        description={t("dataimport.description")}
        breadcrumbs={[{ label: t("dataimport.breadcrumb") }, { label: t("dataimport.title") }]}
      />

      {kindsQ.isError ? (
        <Card><ErrorState error={kindsQ.error} retry={() => kindsQ.refetch()} /></Card>
      ) : kindsQ.isLoading ? (
        <Card><div className="flex justify-center py-16"><Spinner /></div></Card>
      ) : kinds.length === 0 || !kind ? (
        <Card><EmptyState icon={<Upload />} title={t("dataimport.noKinds")} /></Card>
      ) : (
        <div className="space-y-6">
          {/* 1 — what to import */}
          <Card>
            <CardHeader title={t("dataimport.step1")} />
            <div role="radiogroup" aria-label={t("dataimport.step1")} onKeyDown={kindKeys.onKeyDown} className="grid gap-3 p-5 pt-1 sm:grid-cols-2 xl:grid-cols-4">
              {kinds.map((k) => {
                const Icon = KIND_ICONS[k.kind] ?? FileSpreadsheet;
                const active = k.kind === kind.kind;
                return (
                  <button
                    key={k.kind}
                    type="button"
                    role="radio"
                    aria-checked={active}
                    tabIndex={kindKeys.tabIndex(k.kind)}
                    data-value={k.kind}
                    onClick={() => chooseKind(k)}
                    className={cn(
                      "flex items-start gap-3 rounded-xl border p-4 text-left transition focus-visible:ring-2 focus-visible:ring-navy-500 focus-visible:outline-none",
                      active ? "border-navy-600 bg-navy-50/70 ring-1 ring-navy-600" : "border-line bg-white hover:border-slate-300 hover:bg-slate-50",
                    )}
                  >
                    <span className={cn("grid size-10 shrink-0 place-items-center rounded-xl", active ? "bg-navy-800 text-white" : "bg-slate-100 text-slate-600")}>
                      <Icon className="size-5" aria-hidden />
                    </span>
                    <span className="min-w-0">
                      <span className="block font-semibold text-slate-900">{k.label}</span>
                      <span className="mt-0.5 block text-xs leading-snug text-slate-600">{k.description}</span>
                    </span>
                  </button>
                );
              })}
            </div>
            <ColumnHelp kind={kind} />
          </Card>

          {/* 2 — upload */}
          <Card>
            <CardHeader title={t("dataimport.step2")} description={t("dataimport.step2Hint")} />
            <div className="space-y-4 p-5 pt-1">
              <DropZone file={file} onPick={pick} limits={limits} error={fileError ?? checkError} disabled={check.isPending || commit.isPending} />
              <div className="flex flex-wrap items-center gap-2">
                <Button icon={<CheckCircle2 className="size-4" />} disabled={!file} loading={check.isPending} onClick={() => file && check.mutate({ kind: kind.kind, file })}>
                  {t("dataimport.check")}
                </Button>
                {file ? <Button variant="ghost" icon={<X className="size-4" />} onClick={reset}>{t("dataimport.drop.remove")}</Button> : null}
              </div>
            </div>
          </Card>

          {/* 3 — result */}
          {report ? (
            <ResultCard
              report={report}
              kind={kind}
              onCommit={() => setConfirm(true)}
              onAnother={reset}
              committing={commit.isPending}
            />
          ) : null}

          <HistoryCard />
        </div>
      )}

      <ConfirmDialog
        open={confirm && !!report && !!kind}
        onClose={() => setConfirm(false)}
        onConfirm={() => file && kind && commit.mutate({ kind: kind.kind, file })}
        loading={commit.isPending}
        title={t("dataimport.confirmTitle")}
        confirmLabel={t("dataimport.confirmButton")}
        message={report && kind ? t("dataimport.confirmText", { create: report.summary.create, update: report.summary.update, kind: kind.label }) : undefined}
      >
        {changes > 0 && file ? <p className="mt-3 truncate text-xs text-slate-500">{file.name}</p> : null}
      </ConfirmDialog>
    </>
  );
}

// ───────────────────────────── column help ─────────────────────────────

function ColumnHelp({ kind }: { kind: ImportKind }) {
  const { t } = useT();
  const columns: Column<ImportKind["columns"][number]>[] = [
    { key: "name", header: t("dataimport.col.name"), cell: (c) => <code className="rounded bg-slate-100 px-1.5 py-0.5 font-mono text-xs font-semibold text-slate-800">{c.name}</code> },
    {
      key: "required",
      header: t("dataimport.col.required"),
      cell: (c) => (c.required ? <Badge tone="amber">{t("dataimport.required")}</Badge> : <span className="text-xs text-slate-500">{t("dataimport.optional")}</span>),
    },
    { key: "type", header: t("dataimport.col.type"), cell: (c) => <span className="text-xs whitespace-nowrap text-slate-600">{c.typeLabel}</span>, hideOnMobile: true },
    {
      key: "description",
      header: t("dataimport.col.description"),
      cell: (c) => (
        <span className="text-xs text-slate-600">
          {c.description}
          {c.values?.length ? <span className="mt-0.5 block text-slate-500">{t("dataimport.values", { values: c.values.join(", ") })}</span> : null}
        </span>
      ),
    },
    { key: "example", header: t("dataimport.col.example"), cell: (c) => <span className="font-mono text-xs text-slate-700">{c.example || "—"}</span>, hideOnMobile: true },
  ];
  return (
    <div className="border-t border-line">
      <div className="flex flex-wrap items-center justify-between gap-3 px-5 py-3">
        <div>
          <h3 className="text-sm font-semibold text-slate-900">{t("dataimport.columns")} — {kind.label}</h3>
          <p className="text-xs text-slate-500">{t("dataimport.matchRule")}</p>
        </div>
        <Button variant="secondary" size="sm" icon={<Download className="size-4" />} onClick={() => downloadTemplate(kind.kind)}>{t("dataimport.template")}</Button>
      </div>
      <DataTable columns={columns} rows={kind.columns} rowKey={(c) => c.name} />
    </div>
  );
}

// ───────────────────────────── drop zone ─────────────────────────────

function DropZone({ file, onPick, limits, error, disabled }: {
  file: File | null;
  onPick: (f: File | null | undefined) => void;
  limits: { maxBytes: number; maxRows: number };
  error: string | null;
  disabled?: boolean;
}) {
  const { t } = useT();
  const input = useRef<HTMLInputElement>(null);
  const [over, setOver] = useState(false);
  const onDrop = (e: DragEvent) => {
    e.preventDefault();
    setOver(false);
    if (!disabled) onPick(e.dataTransfer.files?.[0]);
  };
  return (
    <div>
      <div
        onDragOver={(e) => {
          e.preventDefault();
          setOver(true);
        }}
        onDragLeave={() => setOver(false)}
        onDrop={onDrop}
        className={cn(
          "flex flex-col items-center justify-center gap-2 rounded-2xl border-2 border-dashed px-6 py-8 text-center transition",
          over ? "border-navy-500 bg-navy-50/60" : error ? "border-red-300 bg-red-50/40" : "border-slate-300 bg-slate-50/50",
        )}
      >
        {file ? (
          <>
            <span className="grid size-12 place-items-center rounded-2xl bg-emerald-50 text-emerald-700"><FileSpreadsheet className="size-6" aria-hidden /></span>
            <p className="max-w-full truncate font-semibold text-slate-900">{file.name}</p>
            <p className="text-xs text-slate-500">{formatInt(Math.ceil(file.size / 1024))} KB</p>
          </>
        ) : (
          <>
            <span className="grid size-12 place-items-center rounded-2xl bg-navy-50 text-navy-700"><FileUp className="size-6" aria-hidden /></span>
            <p className="font-semibold text-slate-800">{t("dataimport.drop.title")}</p>
            <p className="text-xs text-slate-500">{t("dataimport.drop.or")}</p>
          </>
        )}
        <Button variant="secondary" size="sm" disabled={disabled} onClick={() => input.current?.click()} icon={<Upload className="size-4" />}>
          {t("dataimport.drop.choose")}
        </Button>
        <input
          ref={input}
          type="file"
          accept=".csv,text/csv,text/plain"
          className="sr-only"
          tabIndex={-1}
          aria-hidden
          onChange={(e) => {
            onPick(e.target.files?.[0]);
            e.target.value = "";
          }}
        />
        <p className="text-xs text-slate-500">{t("dataimport.drop.limits", { size: mb(limits.maxBytes), rows: formatInt(limits.maxRows) })}</p>
      </div>
      {error ? <p role="alert" className="mt-2 flex items-center gap-1.5 text-sm font-medium text-red-700"><AlertTriangle className="size-4 shrink-0" aria-hidden />{error}</p> : null}
    </div>
  );
}

// ───────────────────────────── result ─────────────────────────────

function ResultCard({ report, kind, onCommit, onAnother, committing }: {
  report: ImportReport;
  kind: ImportKind;
  onCommit: () => void;
  onAnother: () => void;
  committing: boolean;
}) {
  const { t } = useT();
  const s = report.summary;
  const [filter, setFilter] = useState<Filter>(s.errors > 0 ? "errors" : "all");
  const rows = useMemo(() => {
    switch (filter) {
      case "errors":
        return report.items.filter((i) => i.errors.length > 0);
      case "warnings":
        return report.items.filter((i) => i.warnings.length > 0);
      case "changes":
        return report.items.filter((i) => i.action === "create" || i.action === "update");
      default:
        return report.items;
    }
  }, [report.items, filter]);
  const shown = rows.slice(0, MAX_SHOWN);
  const changes = s.create + s.update;

  const columns: Column<ImportItem>[] = [
    { key: "row", header: t("dataimport.table.row"), align: "right", cell: (i) => <span className="font-mono text-xs">{i.row}</span>, className: "w-16" },
    { key: "result", header: t("dataimport.table.result"), cell: (i) => <Badge tone={ACTION_TONE[i.action]}>{t(`dataimport.action.${i.action}`)}</Badge> },
    {
      key: "key",
      header: t("dataimport.table.key"),
      cell: (i) => (
        <div className="min-w-0">
          <p className="font-mono text-xs font-semibold text-slate-800">{i.key || "—"}</p>
          {i.label ? <p className="truncate text-xs text-slate-500 md:hidden">{i.label}</p> : null}
        </div>
      ),
    },
    { key: "name", header: t("dataimport.table.name"), cell: (i) => <span className="text-sm">{i.label || "—"}</span>, hideOnMobile: true },
    {
      key: "messages",
      header: t("dataimport.table.messages"),
      cell: (i) => <Messages item={i} />,
    },
  ];

  return (
    <Card>
      <CardHeader
        title={t("dataimport.step3")}
        description={<span className="break-all">{report.fileName || kind.label} · {t("dataimport.fingerprint", { hash: report.fileHash.slice(0, 16) + "…" })}</span>}
        actions={
          <div className="flex flex-wrap gap-2">
            {report.committed ? <Badge tone="green" dot>{t("dataimport.imported")}</Badge> : null}
            <Button variant="secondary" size="sm" icon={<Download className="size-4" />} onClick={() => downloadReport(report.id)}>{t("dataimport.report")}</Button>
          </div>
        }
      />
      <div className="space-y-5 p-5 pt-1">
        <div className="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6">
          <StatCard label={t("dataimport.stat.rows")} value={formatInt(s.total)} icon={<FileSpreadsheet />} />
          <StatCard label={t("dataimport.stat.create")} value={formatInt(s.create)} icon={<PlusCircle />} tone="green" />
          <StatCard label={t("dataimport.stat.update")} value={formatInt(s.update)} icon={<RefreshCw />} tone="blue" />
          <StatCard label={t("dataimport.stat.unchanged")} value={formatInt(s.unchanged)} icon={<CircleDashed />} />
          <StatCard label={t("dataimport.stat.errors")} value={formatInt(s.errors)} icon={<XCircle />} tone={s.errors ? "red" : "navy"} />
          <StatCard label={t("dataimport.stat.warnings")} value={formatInt(s.warnings)} icon={<AlertTriangle />} tone={s.warnings ? "amber" : "navy"} />
        </div>

        {report.warnings.length ? (
          <Alert tone="amber" title={t("dataimport.fileNotes")}>
            <ul className="list-disc pl-5">{report.warnings.map((w) => <li key={w}>{w}</li>)}</ul>
          </Alert>
        ) : null}

        {report.committed ? (
          <Alert tone="green">{t("dataimport.importedOn", { when: formatDateTime(report.createdAt) })}</Alert>
        ) : s.errors > 0 ? (
          <Alert tone="red">{t("dataimport.blocked")}</Alert>
        ) : changes === 0 ? (
          <Alert tone="blue">{t("dataimport.nothingToDo")}</Alert>
        ) : (
          <Alert tone="green">{t("dataimport.ready")}</Alert>
        )}

        <div className="flex flex-wrap items-center justify-between gap-3">
          <Tabs<Filter>
            value={filter}
            onChange={setFilter}
            tabs={[
              { value: "all", label: t("dataimport.filter.all"), count: s.total },
              { value: "errors", label: t("dataimport.filter.errors"), count: s.errors },
              { value: "warnings", label: t("dataimport.filter.warnings"), count: s.warnings },
              { value: "changes", label: t("dataimport.filter.changes"), count: changes },
            ]}
            className="max-w-full overflow-x-auto"
          />
          <div className="flex flex-wrap gap-2">
            {report.committed ? (
              <Button variant="secondary" icon={<Upload className="size-4" />} onClick={onAnother}>{t("dataimport.another")}</Button>
            ) : (
              <Button variant="accent" icon={<Upload className="size-4" />} disabled={s.errors > 0 || changes === 0} loading={committing} onClick={onCommit}>
                {t("dataimport.commit", { count: changes })}
              </Button>
            )}
          </div>
        </div>
      </div>
      <DataTable columns={columns} rows={shown} rowKey={(i) => String(i.row)} empty={t("dataimport.noRows")} />
      {rows.length > MAX_SHOWN ? <p className="border-t border-line px-4 py-3 text-xs text-slate-500">{t("dataimport.truncated", { shown: MAX_SHOWN, total: rows.length })}</p> : null}
    </Card>
  );
}

function Messages({ item }: { item: ImportItem }) {
  const { t } = useT();
  if (!item.errors.length && !item.warnings.length) {
    if (item.action === "update" && item.changes?.length) {
      return <span className="text-xs text-blue-800">{t("dataimport.table.changed", { columns: item.changes.join(", ") })}</span>;
    }
    return <span className="text-xs text-slate-500">{item.action === "unchanged" ? "—" : t("dataimport.table.ready")}</span>;
  }
  return (
    <ul className="space-y-1">
      {item.errors.map((e, i) => (
        <li key={`e${i}`} className="flex items-start gap-1.5 text-xs text-red-800">
          <XCircle className="mt-px size-3.5 shrink-0 text-red-600" aria-label={t("dataimport.action.error")} />
          <span>{e.field ? <code className="mr-1 font-mono font-semibold">{e.field}</code> : null}{e.message}</span>
        </li>
      ))}
      {item.warnings.map((w, i) => (
        <li key={`w${i}`} className="flex items-start gap-1.5 text-xs text-amber-900">
          <AlertTriangle className="mt-px size-3.5 shrink-0 text-amber-600" aria-label={t("dataimport.filter.warnings")} />
          <span>{w.field ? <code className="mr-1 font-mono font-semibold">{w.field}</code> : null}{w.message}</span>
        </li>
      ))}
    </ul>
  );
}

// ───────────────────────────── history ─────────────────────────────

function HistoryCard() {
  const { t } = useT();
  const [page, setPage] = useState(1);
  const q = useImportHistory(page);
  const columns: Column<HistoryRow>[] = [
    { key: "when", header: t("dataimport.h.when"), cell: (r) => <span className="text-xs whitespace-nowrap">{formatDateTime(r.createdAt)}</span> },
    { key: "kind", header: t("dataimport.h.kind"), cell: (r) => <span className="font-medium text-slate-900">{r.kindLabel}</span> },
    { key: "file", header: t("dataimport.h.file"), cell: (r) => <span className="block max-w-[14rem] truncate text-xs" title={r.fileName}>{r.fileName || "—"}</span>, hideOnMobile: true },
    {
      key: "result",
      header: t("dataimport.h.result"),
      cell: (r) => (
        <span className="text-xs text-slate-600">
          {t("dataimport.h.summary", { create: r.summary.create, update: r.summary.update, unchanged: r.summary.unchanged, errors: r.summary.errors })}
        </span>
      ),
      hideOnMobile: true,
    },
    { key: "by", header: t("dataimport.h.by"), cell: (r) => <span className="text-xs">{r.user?.name ?? "—"}</span>, hideOnMobile: true },
    {
      key: "status",
      header: t("dataimport.h.status"),
      cell: (r) => (r.committed ? <Badge tone="green" dot>{t("dataimport.imported")}</Badge> : <Badge tone="slate">{t("dataimport.checked")}</Badge>),
    },
    {
      key: "report",
      header: <span className="sr-only">{t("dataimport.report")}</span>,
      align: "right",
      cell: (r) => (
        <Button variant="ghost" size="icon" aria-label={`${t("dataimport.report")} — ${r.fileName || r.kindLabel}`} title={t("dataimport.report")} onClick={() => downloadReport(r.id)}>
          <Download className="size-4" />
        </Button>
      ),
    },
  ];
  return (
    <Card>
      <CardHeader title={<span className="flex items-center gap-2"><History className="size-4 text-slate-500" aria-hidden />{t("dataimport.history")}</span>} description={t("dataimport.historyHint")} />
      {q.isError ? (
        <ErrorState error={q.error} retry={() => q.refetch()} />
      ) : (
        <>
          <DataTable columns={columns} rows={q.data?.items} rowKey={(r) => r.id} loading={q.isLoading} empty={t("dataimport.historyEmpty")} />
          {q.data ? <Pagination page={q.data.page} pageSize={q.data.pageSize} total={q.data.total} onPage={setPage} /> : null}
        </>
      )}
    </Card>
  );
}
