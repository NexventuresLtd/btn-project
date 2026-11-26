import { useState } from "react";
import { LogOut, Smartphone } from "lucide-react";
import { Badge, Button, Card, ConfirmDialog, DataTable, EmptyState, ErrorState, PageHeader, Pagination, Select, type Column } from "@/components/ui";
import { useT } from "@/i18n";
import { formatDateTime } from "@/lib/dates";
import { useMutate } from "@/hooks/useMutate";
import { useAuth } from "@/auth/AuthProvider";
import { SearchInput, useDebounced } from "@/modules/admin/components";
import { ADMIN_DEVICES_KEY, MONITOR_KEY, adminRevokeDevice, useAdminDevices, type Device } from "./api";
import { DeviceIcon } from "./DevicesPage";

/** /admin/devices — every user's devices (devices.manage revokes; admin.monitor may view). */
export function AdminDevicesPage() {
  const { t, tx } = useT();
  const { can } = useAuth();
  const [search, setSearch] = useState("");
  const q = useDebounced(search.trim());
  const [status, setStatus] = useState("");
  const [page, setPage] = useState(1);
  const list = useAdminDevices({ q, status, page });
  const [revoking, setRevoking] = useState<Device | null>(null);
  const revoke = useMutate((id: string) => adminRevokeDevice(id), { success: t("session.devices.revokedToast"), invalidate: [ADMIN_DEVICES_KEY, MONITOR_KEY], onSuccess: () => setRevoking(null) });

  const columns: Column<Device>[] = [
    {
      key: "user", header: t("session.adminDevices.user"),
      cell: (d) => (
        <div className="min-w-0">
          <p className="truncate font-medium text-slate-800">{d.user?.name}</p>
          <p className="truncate text-xs text-slate-500">{d.user?.email} · {tx(`enums.role.${d.user?.role}`, undefined, d.user?.role)}{d.user?.plant ? ` · ${d.user.plant.code}` : ""}</p>
        </div>
      ),
    },
    {
      key: "device", header: t("session.adminDevices.device"),
      cell: (d) => (
        <div className="flex items-center gap-2">
          <span className="text-slate-400"><DeviceIcon d={d} className="size-4" /></span>
          <div className="min-w-0">
            <p className="truncate text-slate-800">{d.name}</p>
            <p className="truncate font-mono text-[11px] text-slate-400">{d.deviceId}</p>
          </div>
        </div>
      ),
    },
    { key: "status", header: t("session.adminDevices.status"), cell: (d) => (d.status === "revoked" ? <Badge tone="red">{t("session.devices.revoked")}</Badge> : <Badge tone="green" dot>{t("session.devices.active")}</Badge>) },
    { key: "seen", header: t("session.adminDevices.lastSeen"), hideOnMobile: true, cell: (d) => <span className="tabular-nums">{formatDateTime(d.lastSeenAt)}</span> },
    { key: "sync", header: t("session.adminDevices.lastSync"), hideOnMobile: true, cell: (d) => <span className="tabular-nums">{d.lastSyncAt ? formatDateTime(d.lastSyncAt) : "—"}</span> },
    {
      key: "actions", header: "", align: "right",
      cell: (d) => (d.status === "active" && can("devices.manage") ? (
        <Button size="sm" variant="ghost" className="text-red-600 hover:bg-red-50" icon={<LogOut className="size-4" />} onClick={() => setRevoking(d)}>{t("session.devices.revoke")}</Button>
      ) : null),
    },
  ];

  return (
    <>
      <PageHeader title={t("session.adminDevices.title")} description={t("session.adminDevices.description")} breadcrumbs={[{ label: t("admin.breadcrumb") }, { label: t("session.adminDevices.title") }]} />
      <Card>
        <div className="flex flex-wrap gap-3 px-5 py-4">
          <SearchInput value={search} onChange={(v) => { setSearch(v); setPage(1); }} placeholder={t("session.adminDevices.search")} className="min-w-56 flex-1" />
          <Select value={status} onChange={(e) => { setStatus(e.target.value); setPage(1); }} aria-label={t("session.adminDevices.status")} className="w-48">
            <option value="">{t("session.adminDevices.allStatuses")}</option>
            <option value="active">{t("session.devices.active")}</option>
            <option value="revoked">{t("session.devices.revoked")}</option>
          </Select>
        </div>
        {list.error ? (
          <ErrorState error={list.error} retry={() => void list.refetch()} />
        ) : (
          <DataTable columns={columns} rows={list.data?.items} rowKey={(d) => d.id} loading={list.isLoading}
            empty={<EmptyState icon={<Smartphone />} title={t("session.adminDevices.emptyTitle")} />} />
        )}
        {list.data && list.data.total > list.data.pageSize ? <Pagination page={page} pageSize={list.data.pageSize} total={list.data.total} onPage={setPage} /> : null}
      </Card>
      <ConfirmDialog open={!!revoking} onClose={() => setRevoking(null)} tone="danger" title={t("session.devices.revokeTitle")}
        message={t("session.devices.revokeText", { name: revoking ? `${revoking.name} (${revoking.user?.name ?? ""})` : "" })}
        confirmLabel={t("session.devices.revoke")} loading={revoke.isPending} onConfirm={() => revoking && revoke.mutate(revoking.id)} />
    </>
  );
}
