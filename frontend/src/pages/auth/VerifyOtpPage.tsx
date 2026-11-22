import { useEffect, useState } from "react";
import { Link, useLocation, useNavigate, useSearchParams } from "react-router";
import { ArrowLeft, MailCheck } from "lucide-react";
import { AuthHeading, AuthLayout } from "@/layouts/AuthLayout";
import { Alert, Button } from "@/components/ui";
import { ApiError, post } from "@/lib/api";
import { tr, useT } from "@/i18n";
import { useAuth, type CurrentUser } from "@/auth/AuthProvider";
import { OtpInput } from "./OtpInput";

const RESEND_SECONDS = 60;

export function VerifyOtpPage() {
  const { t } = useT();
  const [params] = useSearchParams();
  const location = useLocation();
  const navigate = useNavigate();
  const { setUser } = useAuth();
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [cooldown, setCooldown] = useState(RESEND_SECONDS);
  const [expiresIn, setExpiresIn] = useState<number>((location.state as { expiresIn?: number } | null)?.expiresIn ?? 600);

  useEffect(() => {
    const timer = setInterval(() => {
      setCooldown((c) => Math.max(0, c - 1));
      setExpiresIn((s) => Math.max(0, s - 1));
    }, 1000);
    return () => clearInterval(timer);
  }, []);

  async function verify(value = code) {
    if (value.length !== 6 || busy) return;
    setBusy(true);
    setError(null);
    try {
      const r = await post<{ user: CurrentUser }>("/auth/otp/verify", { code: value });
      setUser(r.user);
      navigate(r.user.mustChangePassword ? "/account/password" : params.get("next") || "/", { replace: true });
    } catch (err) {
      const e = err instanceof ApiError ? err : new ApiError(0, "network", tr("common.networkError"));
      setError(e);
      setCode("");
      if (e.status === 410) setTimeout(() => navigate("/login", { replace: true }), 2500);
    } finally {
      setBusy(false);
    }
  }

  async function resend() {
    setError(null);
    setNotice(null);
    try {
      const r = await post<{ expiresInSeconds: number }>("/auth/otp/resend");
      setCooldown(RESEND_SECONDS);
      setExpiresIn(r.expiresInSeconds);
      setNotice(t("auth.verify.resent"));
    } catch (err) {
      setError(err instanceof ApiError ? err : new ApiError(0, "network", tr("common.networkError")));
    }
  }

  const mm = String(Math.floor(expiresIn / 60)).padStart(1, "0");
  const ss = String(expiresIn % 60).padStart(2, "0");

  return (
    <AuthLayout>
      <span className="mb-6 grid size-14 place-items-center rounded-2xl bg-navy-50 text-navy-700 ring-1 ring-navy-100"><MailCheck className="size-7" /></span>
      <AuthHeading
        title={t("auth.verify.title")}
        subtitle={t("auth.verify.subtitle", { email: params.get("email") ?? t("auth.verify.yourEmail") })}
      />
      {error ? <div className="mb-5"><Alert tone="red">{error.message}</Alert></div> : null}
      {notice ? <div className="mb-5"><Alert tone="green">{notice}</Alert></div> : null}
      <form onSubmit={(e) => { e.preventDefault(); void verify(); }} className="space-y-6">
        <OtpInput value={code} onChange={setCode} invalid={!!error} disabled={busy} onComplete={(v) => void verify(v)} />
        <p className="text-center text-[13px] text-slate-500">
          {expiresIn > 0 ? <>{t("auth.verify.expiresIn", { time: `${mm}:${ss}` })}</> : <span className="font-semibold text-red-600">{t("auth.verify.expired")}</span>}
        </p>
        <Button type="submit" size="lg" loading={busy} disabled={code.length !== 6} className="w-full">{t("auth.verify.submit")}</Button>
      </form>
      <div className="mt-6 flex items-center justify-between text-sm">
        <Link to="/login" className="inline-flex items-center gap-1.5 font-medium text-slate-500 hover:text-slate-800"><ArrowLeft className="size-4" /> {t("auth.verify.another")}</Link>
        <button onClick={resend} disabled={cooldown > 0} className="font-semibold text-navy-700 hover:text-navy-900 disabled:cursor-not-allowed disabled:text-slate-400">
          {cooldown > 0 ? t("auth.verify.resendIn", { seconds: cooldown }) : t("auth.verify.resend")}
        </button>
      </div>
      <p className="mt-8 text-xs leading-relaxed text-slate-400">{t("auth.verify.spam")}</p>
    </AuthLayout>
  );
}
