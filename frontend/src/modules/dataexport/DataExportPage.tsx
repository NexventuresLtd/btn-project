import { useState } from "react";
import { BookOpen, Database, Download, FileArchive, ShieldAlert } from "lucide-react";
import { toast } from "sonner";
import { useT } from "@/i18n";
import { Alert, Badge, Button, Card, CardHeader, Checkbox, DataTable, EmptyState, ErrorState, Field, Input, PageHeader, Select, type Column } from "@/components/ui";
import { apiUrl, downloadFile } from "@/lib/api";
import { monthStartISO, todayISO } from "@/lib/dates";
import { formatInt } from "@/lib/format";
import { allPath, tablePath, useExportTables, type BulkFormat, type ExportTable } from "./api";

export function DataExportPage() {
  const { t } = useT();
  const q = useExportTables();
  const [format, setFormat] = useState<BulkFormat>("csv");
  const [limit, setLimit] = useState(false);
  const [from, setFrom] = useState(monthStartISO());
  const [to, setTo] = useState(todayISO());
  const range = limit ? { from, to } : {};

  const start = (path: string) => {
    downloadFile(path);
    toast.success(t("exports.data.started"));
  };

  const columns: Column<ExportTable>[] = [
    {
      key: "table",
      header: t("exports.data.col.table"),
      cell: (r) => (
        <div className="min-w-0">
          <p className="font-medium text-slate-800">{r.label}</p>
          <div className="mt-1 flex flex-wrap gap-1.5">
            {r.personal ? <Badge tone="amber">{t("exports.data.personal")}</Badge> : null}
            {r.dateFilter ? <Badge tone="blue">{t("exports.data.dated")}</Badge> : null}
          </div>
        </div>
      ),
    },
    { key: "rows", header: t("exports.data.col.rows"), align: "right", cell: (r) => <span className="tabular">{formatInt(r.rows)}</span> },
    {
      key: "columns",
      header: t("exports.data.col.columns"),
      hideOnMobile: true,
      cell: (r) => <p className="line-clamp-2 max-w-md text-xs text-slate-500" title={r.columns.join(", ")}>{r.columns.join(", ")}</p>,
    },
    {
      key: "download",
      header: <span className="sr-only">{t("exports.data.download")}</span>,
      align: "right",
      cell: (r) => (
        <Button size="sm" variant="secondary" icon={<Download className="size-3.5" />} onClick={() => start(tablePath(r.name, format, range))}
          aria-label={t("exports.data.downloadAria", { table: r.label, format: format.toUpperCase() })}>
          {format.toUpperCase()}
        </Button>
      ),
    },
  ];

  return (
    <>
      <PageHeader
        title={t("exports.data.title")}
        description={t("exports.data.description")}
        breadcrumbs={[{ label: t("admin.breadcrumb") }, { label: t("exports.data.title") }]}
        actions={<Button variant="primary" icon={<FileArchive className="size-4" />} onClick={() => start(allPath(format, range))} disabled={!q.data?.items.length}>{t("exports.data.all")}</Button>}
      />
      <div className="space-y-4">
        <Alert tone="amber" title={t("exports.data.personal")}>
          <span className="flex gap-2"><ShieldAlert className="mt-0.5 size-4 shrink-0" aria-hidden="true" />{t("exports.data.personalNote")}</span>
        </Alert>
        <Card>
          <div className="grid gap-4 px-5 py-4 sm:grid-cols-[12rem_1fr]">
            <Field label={t("exports.data.format")} htmlFor="dx-format">
              <Select id="dx-format" value={format} onChange={(e) => setFormat(e.target.value as BulkFormat)}>
                <option value="csv">CSV</option>
                <option value="json">JSON</option>
              </Select>
            </Field>
            <div className="space-y-2">
              <Checkbox label={t("exports.data.limit")} checked={limit} onChange={(e) => setLimit(e.target.checked)} />
              {limit ? (
                <div className="flex flex-wrap items-center gap-2">
                  <Input type="date" value={from} max={to} onChange={(e) => e.target.value && setFrom(e.target.value)} aria-label={t("common.fromDate")} className="w-44" />
                  <span className="text-slate-400">–</span>
                  <Input type="date" value={to} min={from} onChange={(e) => e.target.value && setTo(e.target.value)} aria-label={t("common.toDate")} className="w-44" />
                </div>
              ) : null}
              <p className="text-xs text-slate-500">{t("exports.data.limitHint")}</p>
            </div>
          </div>
        </Card>
        <Card>
          <CardHeader title={t("exports.data.col.table")} description={t("exports.data.allHint")} />
          {q.isError ? (
            <ErrorState error={q.error} retry={() => q.refetch()} />
          ) : (
            <DataTable columns={columns} rows={q.data?.items} rowKey={(r) => r.name} loading={q.isLoading}
              empty={<EmptyState icon={<Database />} title={t("exports.data.empty")} description={t("exports.data.loadError")} />} />
          )}
        </Card>
        <p className="flex flex-wrap items-center gap-x-4 gap-y-1 text-sm text-slate-500">
          <span>{t("exports.data.reports")}</span>
          <a href={apiUrl("/docs")} target="_blank" rel="noopener" className="inline-flex items-center gap-1 font-medium text-navy-700 hover:underline">
            <BookOpen className="size-4" aria-hidden="true" />{t("exports.data.api")}
          </a>
        </p>
      </div>
    </>
  );
}
