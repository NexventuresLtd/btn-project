import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { useT } from "@/i18n";
import { fieldError } from "@/hooks/useMutate";
import { Button } from "./Button";
import { Field, Textarea } from "./Field";
import { Modal } from "./Modal";

/**
 * Remove / void a record with a mandatory reason (SRS §6.3: records are never deleted; the server marks them removed
 * and keeps them, with who, when and why, for audit). Replaces a plain "Delete?" confirmation.
 */
export function RemoveDialog({ open, onClose, onConfirm, title, message, confirmLabel, loading, error, onlineOnly }: {
  open: boolean;
  onClose: () => void;
  onConfirm: (reason: string) => void;
  title: string;
  message?: ReactNode;
  confirmLabel?: string;
  loading?: boolean;
  error?: unknown;
  onlineOnly?: boolean;
}) {
  const { t } = useT();
  const [reason, setReason] = useState("");
  const [touched, setTouched] = useState(false);
  useEffect(() => {
    if (open) {
      setReason("");
      setTouched(false);
    }
  }, [open]);
  const tooShort = reason.trim().length < 3;
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setTouched(true);
    if (!tooShort) onConfirm(reason.trim());
  };
  const err = (touched && tooShort ? t("records.reasonTooShort") : undefined) ?? fieldError(error, "reason");
  return (
    <Modal open={open} onClose={onClose} title={title} size="sm" footer={
      <>
        <Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
        <Button variant="danger" type="submit" form="remove-reason-form" loading={loading} disabled={tooShort} onlineOnly={onlineOnly}>{confirmLabel ?? t("records.remove")}</Button>
      </>
    }>
      <form id="remove-reason-form" onSubmit={submit} className="space-y-4">
        {message ? <p className="text-sm text-slate-600">{message}</p> : null}
        <Field label={t("records.reason")} htmlFor="remove-reason" required hint={t("records.reasonHint")} error={err}>
          <Textarea id="remove-reason" rows={3} maxLength={500} autoFocus value={reason} placeholder={t("records.reasonPlaceholder")}
            onChange={(e) => setReason(e.target.value)} onBlur={() => setTouched(true)} invalid={!!err} />
        </Field>
      </form>
    </Modal>
  );
}
