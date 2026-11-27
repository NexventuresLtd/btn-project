import { useState } from "react";
import { Cloud, Fingerprint, KeyRound, Pencil, Plus, Smartphone, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { useQueryClient } from "@tanstack/react-query";
import { Alert, Badge, Button, Card, ConfirmDialog, EmptyState, ErrorState, Field, Input, Modal, PageHeader, Spinner } from "@/components/ui";
import { useT } from "@/i18n";
import { formatDateTime } from "@/lib/dates";
import { PasskeyError, passkeyAvailability, registerPasskey } from "@/lib/passkeys";
import { ApiError } from "@/lib/api";
import { useMutate } from "@/hooks/useMutate";
import { ME_KEY, useAuth } from "@/auth/AuthProvider";
import { deletePasskey, PASSKEYS_KEY, renamePasskey, usePasskeys, type Passkey } from "./api";

function defaultName(t: ReturnType<typeof useT>["t"]) {
  const ua = navigator.userAgent;
  const dev = /Android/i.test(ua) ? "Android" : /iPhone|iPad/i.test(ua) ? "iPhone / iPad" : /Mac/i.test(ua) ? "Mac" : /Windows/i.test(ua) ? "Windows" : /Linux/i.test(ua) ? "Linux" : t("security.thisDevice");
  return t("security.defaultName", { device: dev });
}

export function SecurityPage() {
  const { t } = useT();
  const { user } = useAuth();
  const qc = useQueryClient();
  const { data, isLoading, error, refetch } = usePasskeys();
  const [adding, setAdding] = useState(false);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [addError, setAddError] = useState<string | null>(null);
  const [renaming, setRenaming] = useState<Passkey | null>(null);
  const [newName, setNewName] = useState("");
  const [removing, setRemoving] = useState<Passkey | null>(null);
  const issue = passkeyAvailability();

  const rename = useMutate(({ id, name }: { id: string; name: string }) => renamePasskey(id, name), { success: t("security.renamed"), invalidate: [PASSKEYS_KEY], onSuccess: () => setRenaming(null) });
  const remove = useMutate((id: string) => deletePasskey(id), { success: t("security.removed"), invalidate: [PASSKEYS_KEY, ME_KEY], onSuccess: () => setRemoving(null) });

  async function add() {
    setBusy(true);
    setAddError(null);
    try {
      await registerPasskey(name.trim() || defaultName(t));
      toast.success(t("security.added"));
      setAdding(false);
      await qc.invalidateQueries({ queryKey: PASSKEYS_KEY });
      void qc.invalidateQueries({ queryKey: ME_KEY });
    } catch (e) {
      if (e instanceof PasskeyError || e instanceof ApiError) setAddError(e.message);
      else if ((e as { name?: string })?.name === "InvalidStateError") setAddError(t("security.alreadyRegistered"));
      else setAddError(t("auth.passkey.failed"));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="w-full">
      <PageHeader
        title={t("security.title")}
        description={t("security.description")}
        actions={<Button icon={<Plus className="size-4" />} disabled={!!issue} onClick={() => { setName(defaultName(t)); setAddError(null); setAdding(true); }} data-testid="add-passkey">{t("security.add")}</Button>}
      />
      {issue ? <div className="mb-5"><Alert tone="amber">{t(`auth.passkey.${issue}`)}</Alert></div> : null}
      {user?.mustChangePassword ? <div className="mb-5"><Alert tone="blue">{t("security.changePasswordFirst")}</Alert></div> : null}
      <Card>
        <div className="border-b border-line px-5 py-4">
          <h2 className="flex items-center gap-2 text-[15px] font-semibold text-slate-900"><Fingerprint className="size-4 text-navy-600" /> {t("security.passkeys")}</h2>
          <p className="mt-0.5 text-[13px] text-slate-500">{t("security.passkeysHint")}</p>
        </div>
        {isLoading ? (
          <div className="grid place-items-center py-14"><Spinner /></div>
        ) : error ? (
          <ErrorState error={error} retry={() => void refetch()} />
        ) : !data?.length ? (
          <EmptyState icon={<KeyRound />} title={t("security.emptyTitle")} description={t("security.emptyText")} />
        ) : (
          <ul className="divide-y divide-line">
            {data.map((p) => (
              <li key={p.id} className="flex flex-wrap items-center gap-x-4 gap-y-3 px-5 py-4">
                <span className="grid size-10 shrink-0 place-items-center rounded-xl bg-navy-50 text-navy-600"><Smartphone className="size-5" /></span>
                <div className="min-w-0 flex-1 basis-56">
                  <p className="truncate font-semibold text-slate-800">{p.name}</p>
                  <p className="text-[12.5px] text-slate-500">
                    {t("security.created", { date: formatDateTime(p.createdAt) })} · {p.lastUsedAt ? t("security.lastUsed", { date: formatDateTime(p.lastUsedAt) }) : t("security.neverUsed")}
                  </p>
                </div>
                {p.backupState ? <Badge tone="blue"><Cloud className="size-3" /> {t("security.synced")}</Badge> : p.backupEligible ? <Badge tone="slate">{t("security.syncable")}</Badge> : <Badge tone="slate">{t("security.deviceBound")}</Badge>}
                <div className="flex gap-1">
                  <Button size="icon" variant="ghost" aria-label={t("security.rename")} title={t("security.rename")} onClick={() => { setRenaming(p); setNewName(p.name); }}><Pencil className="size-4" /></Button>
                  <Button size="icon" variant="ghost" aria-label={t("common.delete")} title={t("common.delete")} className="text-red-500 hover:bg-red-50 hover:text-red-600" onClick={() => setRemoving(p)}><Trash2 className="size-4" /></Button>
                </div>
              </li>
            ))}
          </ul>
        )}
      </Card>

      <Modal open={adding} onClose={() => !busy && setAdding(false)} title={t("security.addTitle")} description={t("security.addText")} size="sm"
        footer={<><Button variant="secondary" onClick={() => setAdding(false)} disabled={busy}>{t("common.cancel")}</Button><Button loading={busy} onClick={() => void add()}>{t("security.continue")}</Button></>}>
        {addError ? <div className="mb-4"><Alert tone="red">{addError}</Alert></div> : null}
        <Field label={t("security.nameLabel")} hint={t("security.nameHint")} htmlFor="pk-name">
          <Input id="pk-name" value={name} maxLength={60} onChange={(e) => setName(e.target.value)} autoFocus />
        </Field>
      </Modal>

      <Modal open={!!renaming} onClose={() => setRenaming(null)} title={t("security.rename")} size="sm"
        footer={<><Button variant="secondary" onClick={() => setRenaming(null)}>{t("common.cancel")}</Button><Button loading={rename.isPending} disabled={!newName.trim()} onClick={() => renaming && rename.mutate({ id: renaming.id, name: newName.trim() })}>{t("common.save")}</Button></>}>
        <Field label={t("security.nameLabel")} htmlFor="pk-rename"><Input id="pk-rename" value={newName} maxLength={60} onChange={(e) => setNewName(e.target.value)} autoFocus /></Field>
      </Modal>

      <ConfirmDialog open={!!removing} onClose={() => setRemoving(null)} tone="danger" title={t("security.removeTitle")} message={t("security.removeText", { name: removing?.name ?? "" })} confirmLabel={t("common.delete")} loading={remove.isPending} onConfirm={() => removing && remove.mutate(removing.id)} />
    </div>
  );
}
