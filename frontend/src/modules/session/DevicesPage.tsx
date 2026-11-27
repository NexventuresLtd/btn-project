import { useState } from "react";
import { KeyRound, Laptop, LogOut, Pencil, Smartphone, Tablet } from "lucide-react";
import { Badge, Button, Card, ConfirmDialog, EmptyState, ErrorState, Field, Input, Modal, PageHeader, Spinner } from "@/components/ui";
import { useT } from "@/i18n";
import { formatDate, formatDateTime } from "@/lib/dates";
import { useMutate } from "@/hooks/useMutate";
import { ME_KEY, useAuth } from "@/auth/AuthProvider";
import { DEVICES_KEY, renameDevice, revokeMyDevice, useMyDevices, type Device } from "./api";

export function DeviceIcon({ d, className = "size-5" }: { d: Pick<Device, "platform" | "userAgent">; className?: string }) {
  if (d.platform === "Android" || d.platform === "iOS") return /iPad|Tablet/i.test(d.userAgent) ? <Tablet className={className} /> : <Smartphone className={className} />;
  return <Laptop className={className} />;
}

/** /account/devices — the signed-in user's devices (AUTH-06): rename, sign out (revoke). */
export function DevicesPage() {
  const { t } = useT();
  const { logout } = useAuth();
  const { data, isLoading, error, refetch } = useMyDevices();
  const [renaming, setRenaming] = useState<Device | null>(null);
  const [name, setName] = useState("");
  const [revoking, setRevoking] = useState<Device | null>(null);

  const rename = useMutate(({ id, name }: { id: string; name: string }) => renameDevice(id, name), { success: t("session.devices.renamed"), invalidate: [DEVICES_KEY], onSuccess: () => setRenaming(null) });
  const revoke = useMutate((d: Device) => revokeMyDevice(d.id), {
    success: t("session.devices.revokedToast"),
    invalidate: [DEVICES_KEY, ME_KEY],
    onSuccess: (_r, d) => {
      setRevoking(null);
      if (d.current) void logout();
    },
  });

  return (
    <div className="w-full">
      <PageHeader title={t("session.devices.title")} description={t("session.devices.description")} />
      <Card>
        {isLoading ? (
          <div className="grid place-items-center py-14"><Spinner /></div>
        ) : error ? (
          <ErrorState error={error} retry={() => void refetch()} />
        ) : !data?.length ? (
          <EmptyState icon={<Smartphone />} title={t("session.devices.emptyTitle")} description={t("session.devices.emptyText")} />
        ) : (
          <ul className="divide-y divide-line">
            {data.map((d) => (
              <li key={d.id} className="flex flex-wrap items-center gap-x-4 gap-y-3 px-5 py-4" data-testid="device-row">
                <span className={`grid size-10 shrink-0 place-items-center rounded-xl ${d.status === "active" ? "bg-navy-50 text-navy-600" : "bg-slate-100 text-slate-400"}`}><DeviceIcon d={d} /></span>
                <div className="min-w-0 flex-1 basis-60">
                  <p className="flex flex-wrap items-center gap-2 font-semibold text-slate-800">
                    <span className="truncate">{d.name}</span>
                    {d.current ? <Badge tone="blue">{t("session.devices.current")}</Badge> : null}
                    {d.status === "revoked" ? <Badge tone="red">{t("session.devices.revoked")}</Badge> : <Badge tone="green" dot>{t("session.devices.active")}</Badge>}
                  </p>
                  <p className="text-[12.5px] text-slate-500">
                    {t("session.devices.lastSeen", { date: formatDateTime(d.lastSeenAt) })} · {d.lastSyncAt ? t("session.devices.lastSync", { date: formatDateTime(d.lastSyncAt) }) : t("session.devices.neverSynced")}
                  </p>
                  <p className="text-[12.5px] text-slate-500">
                    {t("session.devices.firstSeen", { date: formatDate(d.firstSeenAt) })}
                    {d.status === "active" && d.offlineExpiresAt ? <> · {t("session.devices.offline", { date: formatDate(d.offlineExpiresAt) })}</> : null}
                  </p>
                </div>
                {d.pinSet && d.status === "active" ? <Badge tone="slate"><KeyRound className="size-3" /> {t("session.devices.pin")}</Badge> : null}
                {d.status === "active" ? (
                  <div className="flex gap-1">
                    <Button size="icon" variant="ghost" aria-label={t("session.devices.rename")} title={t("session.devices.rename")} onClick={() => { setRenaming(d); setName(d.name); }}><Pencil className="size-4" /></Button>
                    <Button size="icon" variant="ghost" aria-label={t("session.devices.revoke")} title={t("session.devices.revoke")} className="text-red-600 hover:bg-red-50 hover:text-red-700" onClick={() => setRevoking(d)}><LogOut className="size-4" /></Button>
                  </div>
                ) : null}
              </li>
            ))}
          </ul>
        )}
      </Card>

      <Modal open={!!renaming} onClose={() => setRenaming(null)} title={t("session.devices.rename")} size="sm"
        footer={<><Button variant="secondary" onClick={() => setRenaming(null)}>{t("common.cancel")}</Button><Button loading={rename.isPending} disabled={!name.trim()} onClick={() => renaming && rename.mutate({ id: renaming.id, name: name.trim() })}>{t("common.save")}</Button></>}>
        <Field label={t("session.devices.nameLabel")} hint={t("session.devices.nameHint")} htmlFor="dev-name">
          <Input id="dev-name" value={name} maxLength={60} onChange={(e) => setName(e.target.value)} autoFocus />
        </Field>
      </Modal>

      <ConfirmDialog open={!!revoking} onClose={() => setRevoking(null)} tone="danger" title={t("session.devices.revokeTitle")}
        message={`${t("session.devices.revokeText", { name: revoking?.name ?? "" })}${revoking?.current ? ` ${t("session.devices.revokeCurrent")}` : ""}`}
        confirmLabel={t("session.devices.revoke")} loading={revoke.isPending} onConfirm={() => revoking && revoke.mutate(revoking)} />
    </div>
  );
}
