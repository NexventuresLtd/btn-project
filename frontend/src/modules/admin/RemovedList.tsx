import { useState } from "react";
import { RotateCcw } from "lucide-react";
import { useT } from "@/i18n";
import { Button, Card, DataTable } from "@/components/ui";
import { formatDateTime } from "@/lib/dates";
import type { RemovedRow } from "./api";

/**
 * "Show removed (n)" toggle + the removed rows with who, when, why and a Restore button (SRS §6.3: master data is
 * never deleted, only removed and kept). Renders nothing while there is nothing removed.
 */
export function RemovedList({ items, onRestore, restoringId, className, alwaysOpen }: {
  items: RemovedRow[] | undefined;
  /** show the rows without the toggle (e.g. inside a "Removed" dialog) */
  alwaysOpen?: boolean;
  onRestore?: (row: RemovedRow) => void;
  restoringId?: string;
  className?: string;
}) {
  const { t } = useT();
  const [toggled, setOpen] = useState(false);
  const open = alwaysOpen || toggled;
  if (!items?.length) return null;
  const withCode = items.some((r) => r.code);
  return (
    <div className={className ?? "mt-4"}>
      {alwaysOpen ? null : (
        <Button variant="ghost" size="sm" onClick={() => setOpen((v) => !v)} aria-expanded={open}>
          {open ? t("records.hideRemoved") : t("records.showRemoved", { count: items.length })}
        </Button>
      )}
      {open ? (
        <Card className={alwaysOpen ? "border-0 shadow-none" : "mt-2"}>
          <p className="px-5 pt-4 text-sm font-medium text-slate-700">{t("records.removedTitle")}</p>
          <DataTable
            columns={[
              ...(withCode ? [{ key: "code", header: t("admin.md.field.code"), cell: (r: RemovedRow) => <span className="font-mono text-xs font-semibold text-slate-500">{r.code ?? "—"}</span> }] : []),
              { key: "name", header: t("admin.md.field.name"), cell: (r) => <span className="text-slate-500 line-through">{r.name}</span> },
              { key: "reason", header: t("records.reason"), cell: (r) => <span className="text-sm text-slate-600">{r.reason ?? "—"}</span> },
              { key: "by", header: t("records.removed"), hideOnMobile: true, cell: (r) => <span className="text-xs text-slate-500">{t("records.removedBy", { name: r.removedBy || "—", date: formatDateTime(r.removedAt) })}</span> },
              ...(onRestore
                ? [{
                    key: "restore",
                    header: <span className="sr-only">{t("records.restore")}</span>,
                    align: "right" as const,
                    cell: (r: RemovedRow) => (
                      <Button size="sm" variant="secondary" icon={<RotateCcw className="size-3.5" />} loading={restoringId === r.id} onClick={() => onRestore(r)}>
                        {t("records.restore")}
                      </Button>
                    ),
                  }]
                : []),
            ]}
            rows={items}
            rowKey={(r) => r.id}
            empty={null}
          />
        </Card>
      ) : null}
    </div>
  );
}
