import { useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { ArrowLeft } from "lucide-react";
import { AuthHeading, AuthLayout } from "@/layouts/AuthLayout";
import { Alert, Button, Field, Input } from "@/components/ui";
import { ApiError, post } from "@/lib/api";
import { tr, useT } from "@/i18n";
import { OtpInput } from "./OtpInput";
import { PasswordRules, passwordValid } from "./PasswordRules";

export function ResetPasswordPage() {
  const { t } = useT();
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const [code, setCode] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const mismatch = confirm.length > 0 && confirm !== password;

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (mismatch || !passwordValid(password) || code.length !== 6) return;
    setBusy(true);
    setError(null);
    try {
      await post("/auth/password/reset", { code, password });
      navigate("/login?reset=1", { replace: true });
    } catch (err) {
      setError(err instanceof ApiError ? err : new ApiError(0, "network", tr("common.networkError")));
    } finally {
      setBusy(false);
    }
  }

  return (
    <AuthLayout>
      <AuthHeading title={t("auth.reset.title")} subtitle={t("auth.reset.subtitle", { email: params.get("email") ?? "" })} />
      {error ? <div className="mb-5"><Alert tone="red">{error.message}</Alert></div> : null}
      <form onSubmit={submit} className="space-y-5" noValidate>
        <Field label={t("auth.reset.code")}>
          <OtpInput value={code} onChange={setCode} invalid={!!error?.fields?.code} />
        </Field>
        <Field label={t("auth.reset.newPassword")} htmlFor="pw" error={error?.fields?.password}>
          <Input id="pw" type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} className="h-12" />
        </Field>
        <PasswordRules password={password} />
        <Field label={t("auth.reset.confirm")} htmlFor="pw2" error={mismatch ? t("auth.reset.mismatch") : undefined}>
          <Input id="pw2" type="password" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} className="h-12" invalid={mismatch} />
        </Field>
        <Button type="submit" size="lg" loading={busy} disabled={code.length !== 6 || !passwordValid(password) || confirm !== password} className="w-full">{t("auth.reset.update")}</Button>
      </form>
      <Link to="/forgot-password" className="mt-6 inline-flex items-center gap-1.5 text-sm font-medium text-slate-500 hover:text-slate-800"><ArrowLeft className="size-4" /> {t("auth.reset.newCode")}</Link>
    </AuthLayout>
  );
}
