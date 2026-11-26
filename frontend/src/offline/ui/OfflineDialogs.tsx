import { useState, useSyncExternalStore } from "react";
import { Download, RefreshCw, ShieldOff, Trash2, Upload } from "lucide-react";
import { toast } from "sonner";
import { Alert, Button, Field, Input, Modal } from "@/components/ui";
import { useT } from "@/i18n";
import { useOfflineState } from "../hooks";
import { answerSignOut, dismissPinSuggestion, getOfflineState, syncNow, wipeUser, DEFAULT_UNLOCK_DAYS } from "../runtime";
import { buildExport, downloadJson, importFile } from "../transfer";
import { closeOfflineDialog, getOfflineDialog, openOfflineDialog, subscribeOfflineDialog } from "./dialogBus";
import { PinForm, UnlockForm } from "./forms";

/** All global offline dialogs, rendered once inside the app shell. */
export function OfflineDialogs() {
  const { t } = useT();
  const st = useOfflineState();
  const dialog = useSyncExternalStore(subscribeOfflineDialog, getOfflineDialog, getOfflineDialog);
  const [syncing, setSyncing] = useState(false);

  const pinOpen = dialog === "pin" || st.pinRequired;
  return (
    <>
      {/* offline PIN: required when the data key has no other durable slot; suggested once otherwise */}
      <Modal open={pinOpen} onClose={() => (st.pinRequired ? undefined : closeOfflineDialog())} title={t("offline.pin.title")} description={st.pinRequired ? t("offline.pin.requiredText") : t("offline.pin.text", { days: DEFAULT_UNLOCK_DAYS })} size="sm">
        <PinForm onDone={closeOfflineDialog} onCancel={st.pinRequired ? undefined : () => { dismissPinSuggestion(); closeOfflineDialog(); }} cancelLabel={t("offline.pin.notNow")} />
      </Modal>

      <Modal open={dialog === "unlock"} onClose={closeOfflineDialog} title={t("offline.unlock.lockedTitle")} description={t("offline.unlock.lockedText")} size="sm">
        <UnlockForm online fixedUserId={st.userId} onUnlocked={closeOfflineDialog} />
      </Modal>

      <ExportModal open={dialog === "export"} onClose={closeOfflineDialog} />
      <ImportModal open={dialog === "import"} onClose={closeOfflineDialog} />

      {/* sign-out guard (OFF-14) */}
      <Modal
        open={!!st.signOutPrompt}
        onClose={() => answerSignOut(false)}
        title={t("offline.signout.title")}
        size="sm"
        footer={
          <>
            <Button variant="secondary" onClick={() => answerSignOut(false)}>{t("common.cancel")}</Button>
            <Button variant="danger" icon={<Trash2 className="size-4" />} onClick={() => answerSignOut(true)} data-testid="signout-confirm">{t("offline.signout.confirm")}</Button>
          </>
        }
      >
        <Alert tone="red">{t("offline.signout.text", { count: st.signOutPrompt?.pending ?? 0 })}</Alert>
        <div className="mt-4 flex flex-wrap gap-2">
          <Button
            size="sm"
            variant="secondary"
            loading={syncing}
            icon={<RefreshCw className="size-3.5" />}
            onClick={async () => {
              setSyncing(true);
              await syncNow();
              setSyncing(false);
              const left = getOfflineState().counts.pending + getOfflineState().counts.issues;
              if (left === 0) answerSignOut(true);
            }}
          >
            {t("offline.signout.syncFirst")}
          </Button>
          <Button size="sm" variant="secondary" icon={<Download className="size-3.5" />} onClick={() => { answerSignOut(false); openOfflineDialog("export"); }}>{t("offline.signout.exportFirst")}</Button>
        </div>
      </Modal>

      {/* revoked device (OFF-12): upload/export what is pending, then erase */}
      <Modal open={st.revoked} onClose={() => {}} title={t("offline.revoked.title")} size="sm">
        <div className="space-y-4">
          <div className="flex gap-3">
            <ShieldOff className="mt-0.5 size-5 shrink-0 text-red-600" />
            <p className="text-sm text-slate-700">{st.userId ? t("offline.revoked.text", { count: st.counts.pending + st.counts.issues }) : t("offline.revoked.wiped")}</p>
          </div>
          <div className="flex flex-wrap justify-end gap-2">
            {st.userId ? (
              <>
                <Button variant="secondary" icon={<Download className="size-4" />} onClick={() => openOfflineDialog("export")}>{t("offline.signout.exportFirst")}</Button>
                <Button variant="danger" icon={<Trash2 className="size-4" />} onClick={() => void wipeUser(st.userId!).then(() => window.location.assign("/login"))}>{t("offline.revoked.erase")}</Button>
              </>
            ) : (
              <Button onClick={() => window.location.assign("/login")}>{t("offline.unlock.signInOnline")}</Button>
            )}
          </div>
        </div>
      </Modal>
    </>
  );
}

function ExportModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { t } = useT();
  const [pass, setPass] = useState("");
  const [again, setAgain] = useState("");
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const submit = async () => {
    if (pass.length < 10) return setErr(t("offline.export.short"));
    if (pass !== again) return setErr(t("offline.export.mismatch"));
    setBusy(true);
    try {
      const { file, count } = await buildExport(pass);
      if (!count) {
        toast.info(t("offline.export.none"));
        return;
      }
      downloadJson(`btn-pending-${new Date().toISOString().slice(0, 16).replace(/[:T]/g, "-")}.btnx.json`, file);
      toast.success(t("offline.export.done", { count }));
      setPass("");
      setAgain("");
      onClose();
    } catch {
      setErr(t("offline.err.locked"));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal open={open} onClose={onClose} title={t("offline.export.title")} description={t("offline.export.text")} size="sm" footer={
      <>
        <Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
        <Button loading={busy} icon={<Download className="size-4" />} onClick={() => void submit()}>{t("offline.export.download")}</Button>
      </>
    }>
      <div className="space-y-4">
        <Field label={t("offline.export.passphrase")} htmlFor="exp-pass" error={err ?? undefined}>
          <Input id="exp-pass" type="password" autoComplete="new-password" value={pass} onChange={(e) => { setPass(e.target.value); setErr(null); }} invalid={!!err} />
        </Field>
        <Field label={t("offline.export.confirm")} htmlFor="exp-again">
          <Input id="exp-again" type="password" autoComplete="new-password" value={again} onChange={(e) => setAgain(e.target.value)} />
        </Field>
      </div>
    </Modal>
  );
}

function ImportModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { t } = useT();
  const [file, setFile] = useState<File | null>(null);
  const [pass, setPass] = useState("");
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const submit = async () => {
    if (!file || !pass) return;
    setBusy(true);
    setErr(null);
    try {
      const r = await importFile(await file.text(), pass);
      if (!r.ok) return setErr(r.error === "wrong" ? t("offline.import.wrong") : r.error === "locked" ? t("offline.err.locked") : t("offline.import.format"));
      toast.success(t("offline.import.done", { count: r.count }));
      void syncNow();
      setFile(null);
      setPass("");
      onClose();
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal open={open} onClose={onClose} title={t("offline.import.title")} description={t("offline.import.text")} size="sm" footer={
      <>
        <Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
        <Button loading={busy} disabled={!file || !pass} icon={<Upload className="size-4" />} onClick={() => void submit()}>{t("offline.import.submit")}</Button>
      </>
    }>
      <div className="space-y-4">
        {err ? <Alert tone="red">{err}</Alert> : null}
        <Field label={t("offline.import.file")} htmlFor="imp-file">
          <input id="imp-file" type="file" accept=".json,application/json" onChange={(e) => setFile(e.target.files?.[0] ?? null)} className="block w-full text-sm text-slate-700 file:mr-3 file:rounded-lg file:border-0 file:bg-navy-50 file:px-3 file:py-2 file:text-sm file:font-semibold file:text-navy-800" />
        </Field>
        <Field label={t("offline.import.passphrase")} htmlFor="imp-pass">
          <Input id="imp-pass" type="password" autoComplete="off" value={pass} onChange={(e) => setPass(e.target.value)} />
        </Field>
      </div>
    </Modal>
  );
}
