import { useEffect, useState, type FormEvent } from "react";
import { KeyRound, Lock } from "lucide-react";
import { toast } from "sonner";
import { Alert, Button, Field, Input, Select } from "@/components/ui";
import { formatDate } from "@/lib/dates";
import { useT } from "@/i18n";
import { MAX_PIN_FAILURES, validatePin } from "../core/vault";
import { deviceUsers, markPinOnServer, setOfflinePin, unlock, DEFAULT_UNLOCK_DAYS, type UnlockError } from "../runtime";
import type { DeviceUser } from "../core/db";

/** New offline PIN (≥ 6 digits). The verifier is the wrapped key itself — nothing is sent to the server except "a PIN exists". */
export function PinForm({ onDone, onCancel, cancelLabel }: { onDone?: () => void; onCancel?: () => void; cancelLabel?: string }) {
  const { t } = useT();
  const [pin, setPin] = useState("");
  const [again, setAgain] = useState("");
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const v = validatePin(pin);
    if (v !== "ok") return setErr(t(`offline.pin.err.${v}`));
    if (pin !== again) return setErr(t("offline.pin.mismatch"));
    setBusy(true);
    try {
      await setOfflinePin(pin);
      // tell the server a PIN exists (keeps the session alive for the offline window); the PIN itself never leaves
      void markPinOnServer(true);
      toast.success(t("offline.pin.saved"));
      onDone?.();
    } catch {
      setErr(t("offline.err.locked"));
    } finally {
      setBusy(false);
    }
  };
  return (
    <form onSubmit={(e) => void submit(e)} className="space-y-4" noValidate>
      <Field label={t("offline.pin.pin")} htmlFor="offline-pin-new" error={err ?? undefined}>
        <Input id="offline-pin-new" type="password" inputMode="numeric" autoComplete="new-password" pattern="[0-9]*" maxLength={12} value={pin} onChange={(e) => setPin(e.target.value.replace(/\D/g, ""))} invalid={!!err} />
      </Field>
      <Field label={t("offline.pin.confirm")} htmlFor="offline-pin-again">
        <Input id="offline-pin-again" type="password" inputMode="numeric" autoComplete="new-password" pattern="[0-9]*" maxLength={12} value={again} onChange={(e) => setAgain(e.target.value.replace(/\D/g, ""))} />
      </Field>
      <div className="flex justify-end gap-2">
        {onCancel ? <Button variant="secondary" onClick={onCancel}>{cancelLabel ?? t("common.cancel")}</Button> : null}
        <Button type="submit" loading={busy} icon={<KeyRound className="size-4" />}>{t("offline.pin.save")}</Button>
      </div>
    </form>
  );
}

const ERR_KEY: Record<UnlockError, "offline.unlock.pinLocked" | "offline.unlock.expired" | "offline.unlock.clock" | "offline.unlock.noData"> = {
  wrong: "offline.unlock.pinLocked", pinLocked: "offline.unlock.pinLocked", expired: "offline.unlock.expired", clock: "offline.unlock.clock", noData: "offline.unlock.noData",
};

/** Choose account + PIN/password → unlock the local vault. `online` skips the offline-window check. */
export function UnlockForm({ online, onUnlocked, fixedUserId }: { online?: boolean; onUnlocked?: () => void; fixedUserId?: string | null }) {
  const { t } = useT();
  const [users, setUsers] = useState<DeviceUser[] | null>(null);
  const [userId, setUserId] = useState(fixedUserId ?? "");
  const [kind, setKind] = useState<"pin" | "password">("pin");
  const [secret, setSecret] = useState("");
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<string | null>(null);
  useEffect(() => {
    void deviceUsers().then((u) => {
      setUsers(u);
      if (!fixedUserId && u[0]) setUserId(u[0].userId);
    });
  }, [fixedUserId]);
  const current = users?.find((u) => u.userId === userId);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!userId || !secret) return;
    setBusy(true);
    setMsg(null);
    try {
      const r = await unlock(userId, secret, kind, { online });
      if (r.ok) {
        setSecret("");
        onUnlocked?.();
        return;
      }
      if (r.error === "wrong") setMsg(kind === "pin" ? t("offline.unlock.wrong", { left: MAX_PIN_FAILURES - (r.failures ?? 0) }) : t("offline.unlock.wrongPassword"));
      else {
        setMsg(t(ERR_KEY[r.error]));
        if (r.error === "pinLocked") setKind("password");
      }
    } finally {
      setBusy(false);
    }
  };

  if (users && users.length === 0 && !fixedUserId) return <Alert tone="blue">{t("offline.unlock.noUsers")}</Alert>;

  return (
    <form onSubmit={(e) => void submit(e)} className="space-y-4" noValidate data-testid="unlock-form">
      {msg ? <Alert tone="red">{msg}</Alert> : null}
      {!fixedUserId && users && users.length > 1 ? (
        <Field label={t("offline.unlock.user")} htmlFor="offline-user">
          <Select id="offline-user" value={userId} onChange={(e) => setUserId(e.target.value)}>
            {users.map((u) => <option key={u.userId} value={u.userId}>{u.name} · {u.email}</option>)}
          </Select>
        </Field>
      ) : current ? <p className="text-sm font-semibold text-slate-800">{current.name} <span className="font-normal text-slate-500">· {current.email}</span></p> : null}
      <Field label={kind === "pin" ? t("offline.unlock.pin") : t("offline.unlock.password")} htmlFor="offline-secret">
        <div className="relative">
          <Lock className="pointer-events-none absolute top-1/2 left-3.5 size-4 -translate-y-1/2 text-slate-400" />
          <Input
            id="offline-secret"
            type="password"
            autoFocus
            inputMode={kind === "pin" ? "numeric" : undefined}
            autoComplete={kind === "pin" ? "one-time-code" : "current-password"}
            value={secret}
            onChange={(e) => setSecret(kind === "pin" ? e.target.value.replace(/\D/g, "") : e.target.value)}
            className="h-12 pl-10"
          />
        </div>
      </Field>
      <Button type="submit" size="lg" className="w-full" loading={busy} disabled={!secret || !userId}>{t("offline.unlock.submit")}</Button>
      <button type="button" className="w-full text-center text-[13px] font-semibold text-navy-700 hover:text-navy-900" onClick={() => { setKind(kind === "pin" ? "password" : "pin"); setSecret(""); setMsg(null); }}>
        {kind === "pin" ? t("offline.unlock.usePassword") : t("offline.unlock.usePin")}
      </button>
      {current?.expiresAt ? <p className="text-center text-xs text-slate-500">{t("offline.unlock.validUntil", { date: formatDate(current.expiresAt) })}</p> : null}
    </form>
  );
}

export const UNLOCK_DAYS = DEFAULT_UNLOCK_DAYS;
