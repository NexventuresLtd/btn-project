import { useState, type FormEvent } from "react";
import { useNavigate } from "react-router";
import { ShieldCheck } from "lucide-react";
import { toast } from "sonner";
import { AuthHeading, AuthLayout } from "@/layouts/AuthLayout";
import { Alert, Button, Field, Input } from "@/components/ui";
import { ApiError, post } from "@/lib/api";
import { tr, useT } from "@/i18n";
import { useAuth, type CurrentUser } from "@/auth/AuthProvider";
import { PasswordRules, passwordValid } from "./PasswordRules";

/** Forced on first sign-in (temporary password) and available from the account menu. */
export function ChangePasswordPage() {
  const { t } = useT();
  const { user, setUser, logout } = useAuth();
  const navigate = useNavigate();
  const [current, setCurrent] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const mismatch = confirm.length > 0 && confirm !== password;
  const forced = !!user?.mustChangePassword;

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (mismatch || !passwordValid(password)) return;
    setBusy(true);
    setError(null);
    try {
      const r = await post<{ user: CurrentUser }>("/auth/password/change", { currentPassword: current, newPassword: password });
      setUser(r.user);
      toast.success(t("auth.change.done"));
      navigate("/", { replace: true });
    } catch (err) {
      setError(err instanceof ApiError ? err : new ApiError(0, "network", tr("common.networkError")));
    } finally {
      setBusy(false);
    }
  }

  return (
    <AuthLayout>
      <span className="mb-6 grid size-14 place-items-center rounded-2xl bg-emerald-50 text-emerald-600 ring-1 ring-emerald-100"><ShieldCheck className="size-7" /></span>
      <AuthHeading
        title={forced ? t("auth.change.forcedTitle") : t("auth.change.title")}
        subtitle={forced ? t("auth.change.forcedSubtitle", { name: user?.name.split(" ")[0] ?? "" }) : t("auth.change.subtitle")}
      />
      {error && !error.fields ? <div className="mb-5"><Alert tone="red">{error.message}</Alert></div> : null}
      <form onSubmit={submit} className="space-y-5" noValidate>
        <Field label={forced ? t("auth.change.temp") : t("auth.change.current")} htmlFor="cur" error={error?.fields?.currentPassword}>
          <Input id="cur" type="password" autoComplete="current-password" autoFocus value={current} onChange={(e) => setCurrent(e.target.value)} className="h-12" invalid={!!error?.fields?.currentPassword} />
        </Field>
        <Field label={t("auth.reset.newPassword")} htmlFor="pw" error={error?.fields?.newPassword}>
          <Input id="pw" type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} className="h-12" invalid={!!error?.fields?.newPassword} />
        </Field>
        <PasswordRules password={password} />
        <Field label={t("auth.reset.confirm")} htmlFor="pw2" error={mismatch ? t("auth.reset.mismatch") : undefined}>
          <Input id="pw2" type="password" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} className="h-12" invalid={mismatch} />
        </Field>
        <Button type="submit" size="lg" loading={busy} disabled={!current || !passwordValid(password) || confirm !== password} className="w-full">{t("auth.change.save")}</Button>
      </form>
      <div className="mt-6 flex justify-between text-sm">
        {!forced ? <button onClick={() => navigate(-1)} className="font-medium text-slate-500 hover:text-slate-800">{t("common.cancel")}</button> : <span />}
        <button onClick={() => void logout().then(() => navigate("/login"))} className="font-medium text-slate-500 hover:text-slate-800">{t("auth.signOut")}</button>
      </div>
    </AuthLayout>
  );
}
