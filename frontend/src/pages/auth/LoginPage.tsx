import { useState, type FormEvent } from "react";
import { Link, Navigate, useNavigate, useSearchParams } from "react-router";
import { Eye, EyeOff, KeyRound, Lock, Mail, ShieldCheck } from "lucide-react";
import { AuthHeading, AuthLayout } from "@/layouts/AuthLayout";
import { Alert, Button, Field, Input } from "@/components/ui";
import { ApiError, post } from "@/lib/api";
import { tr, useT } from "@/i18n";
import { PasskeyError, passkeyAvailability, signInWithPasskey } from "@/lib/passkeys";
import { useAuth } from "@/auth/AuthProvider";

export function LoginPage() {
  const { t } = useT();
  const { user, setUser } = useAuth();
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [show, setShow] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const [passkeyMsg, setPasskeyMsg] = useState<string | null>(null);
  const [passkeyBusy, setPasskeyBusy] = useState(false);

  if (user) return <Navigate to={user.mustChangePassword ? "/account/password" : params.get("next") || "/"} replace />;

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const r = await post<{ email: string; expiresInSeconds: number }>("/auth/login", { email, password });
      navigate(`/login/verify?${new URLSearchParams({ email: r.email, next: params.get("next") ?? "" })}`, { state: { expiresIn: r.expiresInSeconds } });
    } catch (err) {
      setError(err instanceof ApiError ? err : new ApiError(0, "network", tr("common.networkError")));
    } finally {
      setBusy(false);
    }
  }

  async function signInPasskey() {
    setError(null);
    setPasskeyMsg(null);
    const issue = passkeyAvailability();
    if (issue) return setPasskeyMsg(t(`auth.passkey.${issue}`));
    setPasskeyBusy(true);
    try {
      const u = await signInWithPasskey();
      setUser(u);
      navigate(u.mustChangePassword ? "/account/password" : params.get("next") || "/", { replace: true });
    } catch (err) {
      if (err instanceof PasskeyError) setPasskeyMsg(err.message);
      else if (err instanceof ApiError) setPasskeyMsg(err.message);
      else setPasskeyMsg(t("auth.passkey.failed"));
    } finally {
      setPasskeyBusy(false);
    }
  }

  return (
    <AuthLayout>
      <AuthHeading title={t("auth.login.title")} subtitle={t("auth.login.subtitle")} />
      {params.get("reset") === "1" ? <div className="mb-5"><Alert tone="green" title={t("auth.login.resetTitle")}>{t("auth.login.resetText")}</Alert></div> : null}
      {params.get("expired") === "1" ? <div className="mb-5"><Alert tone="blue">{t("auth.login.expired")}</Alert></div> : null}
      {error && !error.fields ? <div className="mb-5"><Alert tone="red">{error.message}</Alert></div> : null}
      <form onSubmit={submit} className="space-y-5" noValidate>
        <Field label={t("auth.email")} htmlFor="email" error={error?.fields?.email}>
          <div className="relative">
            <Mail className="pointer-events-none absolute top-1/2 left-3.5 size-4 -translate-y-1/2 text-slate-400" />
            <Input id="email" type="email" autoComplete="username" autoFocus required value={email} onChange={(e) => setEmail(e.target.value)} placeholder={t("auth.emailPlaceholder")} className="h-12 pl-10" invalid={!!error?.fields?.email} />
          </div>
        </Field>
        <Field
          label={<span className="flex w-full flex-wrap justify-between gap-x-3">{t("auth.password")}<Link to="/forgot-password" className="text-[13px] font-semibold text-navy-700 hover:text-navy-900">{t("auth.forgot")}</Link></span>}
          htmlFor="password"
          error={error?.fields?.password}
        >
          <div className="relative">
            <Lock className="pointer-events-none absolute top-1/2 left-3.5 size-4 -translate-y-1/2 text-slate-400" />
            <Input id="password" type={show ? "text" : "password"} autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.target.value)} placeholder={t("auth.passwordPlaceholder")} className="h-12 pr-11 pl-10" invalid={!!error?.fields?.password} />
            <button type="button" onClick={() => setShow((s) => !s)} className="absolute top-1/2 right-2 -translate-y-1/2 rounded-lg p-2 text-slate-400 hover:text-slate-700" aria-label={show ? t("auth.hidePassword") : t("auth.showPassword")}>
              {show ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
            </button>
          </div>
        </Field>
        <Button type="submit" size="lg" loading={busy} className="w-full">{t("auth.signIn")}</Button>
      </form>
      <div className="my-5 flex items-center gap-3 text-xs text-slate-400"><span className="h-px flex-1 bg-line" />{t("auth.passkey.or")}<span className="h-px flex-1 bg-line" /></div>
      {passkeyMsg ? <div className="mb-3"><Alert tone="amber">{passkeyMsg}</Alert></div> : null}
      <Button type="button" variant="secondary" size="lg" loading={passkeyBusy} onClick={() => void signInPasskey()} icon={<KeyRound className="size-4" />} className="w-full" data-testid="passkey-login">{t("auth.passkey.button")}</Button>
      <div className="mt-8 flex gap-3 rounded-xl border border-navy-100 bg-navy-50/60 p-4">
        <ShieldCheck className="mt-0.5 size-5 shrink-0 text-navy-600" />
        <p className="text-[13px] leading-relaxed text-navy-800/80">
          {t("auth.login.info")}
        </p>
      </div>
    </AuthLayout>
  );
}
