import { useT } from "@/i18n";
import type { ReactNode } from "react";
import { cn } from "@/lib/cn";
import { Spinner } from "./Feedback";

export interface Column<T> {
  key: string;
  header: ReactNode;
  cell: (row: T) => ReactNode;
  align?: "left" | "right" | "center";
  className?: string;
  /** hide below the md breakpoint */
  hideOnMobile?: boolean;
}

export function DataTable<T>({ columns, rows, rowKey, loading, empty, onRowClick, footer, className }: {
  columns: Column<T>[];
  rows: T[] | undefined;
  rowKey: (row: T) => string;
  loading?: boolean;
  empty?: ReactNode;
  onRowClick?: (row: T) => void;
  footer?: ReactNode;
  className?: string;
}) {
  const { t } = useT();
  const align = (a?: string) => (a === "right" ? "text-right" : a === "center" ? "text-center" : "text-left");
  return (
    <div className={cn("overflow-x-auto", className)}>
      <table className="w-full text-sm">
        <thead>
          <tr className="border-y border-line bg-slate-50/70">
            {columns.map((c) => (
              <th key={c.key} className={cn("px-4 py-2.5 text-[11px] font-semibold tracking-wide whitespace-nowrap text-slate-500 uppercase", align(c.align), c.hideOnMobile && "hidden md:table-cell", c.className)}>
                {c.header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody className="divide-y divide-line">
          {loading ? (
            <tr><td colSpan={columns.length} className="py-14"><Spinner className="mx-auto" /></td></tr>
          ) : !rows?.length ? (
            <tr><td colSpan={columns.length} className="px-4 py-12 text-center text-sm text-slate-500">{empty ?? t("common.nothingToShow")}</td></tr>
          ) : (
            rows.map((row) => (
              <tr key={rowKey(row)} onClick={onRowClick ? () => onRowClick(row) : undefined} className={cn("transition-colors", onRowClick && "cursor-pointer hover:bg-navy-50/40")}>
                {columns.map((c) => (
                  <td key={c.key} className={cn("px-4 py-3 align-middle text-slate-700", align(c.align), c.align === "right" && "tabular", c.hideOnMobile && "hidden md:table-cell", c.className)}>
                    {c.cell(row)}
                  </td>
                ))}
              </tr>
            ))
          )}
        </tbody>
        {footer && rows?.length ? <tfoot className="border-t-2 border-navy-100 bg-navy-50/50 font-semibold text-slate-900">{footer}</tfoot> : null}
      </table>
    </div>
  );
}

export function Pagination({ page, pageSize, total, onPage }: { page: number; pageSize: number; total: number; onPage: (p: number) => void }) {
  const { t } = useT();
  const pages = Math.max(1, Math.ceil(total / pageSize));
  if (total <= pageSize) return null;
  return (
    <div className="flex items-center justify-between border-t border-line px-4 py-3 text-xs text-slate-500">
      <span>
        {t("common.pageRange", { from: (page - 1) * pageSize + 1, to: Math.min(page * pageSize, total), total })}
      </span>
      <div className="flex gap-1">
        <button disabled={page <= 1} onClick={() => onPage(page - 1)} className="rounded-lg border border-line px-3 py-1.5 font-medium hover:bg-slate-50 disabled:opacity-40">{t("common.previous")}</button>
        <button disabled={page >= pages} onClick={() => onPage(page + 1)} className="rounded-lg border border-line px-3 py-1.5 font-medium hover:bg-slate-50 disabled:opacity-40">{t("common.next")}</button>
      </div>
    </div>
  );
}
