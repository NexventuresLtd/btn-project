import { useEffect, useState, type FormEvent } from "react";
import { RotateCcw, TriangleAlert } from "lucide-react";
import { Alert, Button, Card, CardBody, CardHeader, Field, Input, Modal } from "@/components/ui";
import { useT } from "@/i18n";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { post } from "@/lib/api";
import { afterSignOut } from "@/offline/runtime";

const resetSystem = (body: { code: string; password: string }) => post<{ ok: boolean; superAdminsKept: number }>("/admin/system/reset", body);

/**
 * Danger zone (Super Administrator only): wipe every record and start again from a fresh install, keeping only the
 * Super Administrator accounts. Needs the confirmation code and the caller's password; everyone is signed out after.
 */
export function SystemResetCard() {
  const { t } = useT();
  const [open, setOpen] = useState(false);
  const [code, setCode] = useState("");
  const [password, setPassword] = useState("");
  const reset = useMutate(resetSystem, {
    success: t("superadmin.reset.done"),
    onSuccess: () => {
      // the server ended every session: drop this browser's local copy too (no "unsynced records" prompt — the
      // records it would keep belong to the wiped system), then start over at the sign-in page
      void afterSignOut().catch(() => {}).finally(() => window.location.assign("/login"));
    },
  });
  useEffect(() => {
    if (open) {
      setCode("");
      setPassword("");
      reset.reset();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- reset only when the dialog opens
  }, [open]);

  const ready = code.trim() !== "" && password !== "";
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (ready) reset.mutate({ code: code.trim(), password });
  };

  return (
    <Card className="border-red-200">
      <CardHeader
        title={<span className="flex items-center gap-2 text-red-700"><TriangleAlert className="size-4" />{t("superadmin.reset.title")}</span>}
        description={t("superadmin.reset.description")}
      />
      <CardBody className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-sm text-slate-600">{t("superadmin.reset.keeps")}</p>
        <Button variant="danger" icon={<RotateCcw className="size-4" />} onClick={() => setOpen(true)} onlineOnly>{t("superadmin.reset.button")}</Button>
      </CardBody>
      <Modal open={open} onClose={() => !reset.isPending && setOpen(false)} title={t("superadmin.reset.dialogTitle")} size="sm" footer={
        <>
          <Button variant="secondary" onClick={() => setOpen(false)} disabled={reset.isPending}>{t("common.cancel")}</Button>
          <Button variant="danger" type="submit" form="system-reset-form" loading={reset.isPending} disabled={!ready} onlineOnly>{t("superadmin.reset.confirm")}</Button>
        </>
      }>
        <form id="system-reset-form" onSubmit={submit} className="space-y-4">
          <Alert tone="red" title={t("superadmin.reset.irreversible")}>
            <p>{t("superadmin.reset.willDelete")}</p>
            <ul className="mt-1 list-disc space-y-0.5 pl-5">
              <li>{t("superadmin.reset.itemRecords")}</li>
              <li>{t("superadmin.reset.itemUsers")}</li>
              <li>{t("superadmin.reset.itemData")}</li>
            </ul>
          </Alert>
          <p className="text-sm text-slate-600">{t("superadmin.reset.restored")}</p>
          <Field label={t("superadmin.reset.codeLabel")} htmlFor="reset-code" required hint={t("superadmin.reset.codeHint")} error={fieldError(reset.error, "code")}>
            <Input id="reset-code" autoComplete="off" autoCapitalize="off" spellCheck={false} autoFocus value={code} onChange={(e) => setCode(e.target.value)} invalid={!!fieldError(reset.error, "code")} />
          </Field>
          <Field label={t("superadmin.reset.passwordLabel")} htmlFor="reset-password" required error={fieldError(reset.error, "password")}>
            <Input id="reset-password" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} invalid={!!fieldError(reset.error, "password")} />
          </Field>
        </form>
      </Modal>
    </Card>
  );
}
