import { useEffect, useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { ArrowLeft } from "lucide-react";
import { AuthHeading, AuthLayout } from "@/layouts/AuthLayout";
import { Alert, Button, Field, Input } from "@/components/ui";
import { ApiError, post } from "@/lib/api";
import { tr, useT } from "@/i18n";
import { OtpInput } from "./OtpInput";
import { PasswordRules, passwordValid } from "./PasswordRules";

export function ResetPasswordPage() {
  const [params] = useSearchParams();
  const token = params.get("token");
  // AUTH-04: the emailed single-use link lands here with ?token=; the 6-digit code stays as an alternative.
  return token ? <LinkReset token={token} /> : <CodeReset />;
}

function CodeReset() {
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
      <p className="-mt-2 mb-5 text-sm text-slate-500">{t("session.reset.linkHint")}</p>
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

function LinkReset({ token }: { token: string }) {
  const { t } = useT();
  const navigate = useNavigate();
  const [state, setState] = useState<"checking" | "valid" | "invalid">("checking");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const mismatch = confirm.length > 0 && confirm !== password;

  useEffect(() => {
    let live = true;
    // Keep the secret out of the address bar, history and Referer headers once it has been read.
    try {
      window.history.replaceState(window.history.state, "", "/reset-password?link=1");
    } catch { /* ignore */ }
    post<{ valid: boolean; email: string }>("/auth/password/reset-link/check", { token })
      .then((r) => {
        if (!live) return;
        setEmail(r.email);
        setState("valid");
      })
      .catch((e) => {
        if (!live) return;
        if (e instanceof ApiError && e.status === 0) setError(e);
        setState("invalid");
      });
    return () => {
      live = false;
    };
  }, [token]);

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (mismatch || !passwordValid(password)) return;
    setBusy(true);
    setError(null);
    try {
      await post("/auth/password/reset-link", { token, password });
      navigate("/login?reset=1", { replace: true });
    } catch (err) {
      if (err instanceof ApiError && err.status === 410) setState("invalid");
      else setError(err instanceof ApiError ? err : new ApiError(0, "network", tr("common.networkError")));
    } finally {
      setBusy(false);
    }
  }

  if (state === "checking") {
    return (
      <AuthLayout>
        <AuthHeading title={t("session.reset.linkTitle")} subtitle={t("session.reset.checking")} />
      </AuthLayout>
    );
  }
  if (state === "invalid") {
    return (
      <AuthLayout>
        <AuthHeading title={t("session.reset.linkInvalidTitle")} subtitle={t("session.reset.linkInvalidText")} />
        {error ? <div className="mb-5"><Alert tone="red">{error.message}</Alert></div> : null}
        <div className="flex flex-col gap-3">
          <Link to="/forgot-password" className="inline-flex h-12 items-center justify-center rounded-xl bg-navy-800 px-4 text-sm font-semibold text-white hover:bg-navy-700">{t("session.reset.requestNew")}</Link>
          <Link to="/reset-password" className="inline-flex items-center justify-center gap-1.5 text-sm font-medium text-slate-600 hover:text-slate-900">{t("session.reset.useCode")}</Link>
        </div>
      </AuthLayout>
    );
  }
  return (
    <AuthLayout>
      <AuthHeading title={t("session.reset.linkTitle")} subtitle={t("session.reset.linkSubtitle", { email })} />
      {error ? <div className="mb-5"><Alert tone="red">{error.message}</Alert></div> : null}
      <form onSubmit={submit} className="space-y-5" noValidate>
        <Field label={t("auth.reset.newPassword")} htmlFor="pw" error={error?.fields?.password}>
          <Input id="pw" type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} className="h-12" autoFocus />
        </Field>
        <PasswordRules password={password} />
        <Field label={t("auth.reset.confirm")} htmlFor="pw2" error={mismatch ? t("auth.reset.mismatch") : undefined}>
          <Input id="pw2" type="password" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} className="h-12" invalid={mismatch} />
        </Field>
        <Button type="submit" size="lg" loading={busy} disabled={!passwordValid(password) || confirm !== password} className="w-full">{t("auth.reset.update")}</Button>
      </form>
      <Link to="/login" className="mt-6 inline-flex items-center gap-1.5 text-sm font-medium text-slate-500 hover:text-slate-800"><ArrowLeft className="size-4" /> {t("auth.forgot.back")}</Link>
    </AuthLayout>
  );
}
