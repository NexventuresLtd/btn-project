import { useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router";
import { ArrowLeft, KeyRound, Mail } from "lucide-react";
import { AuthHeading, AuthLayout } from "@/layouts/AuthLayout";
import { Alert, Button, Field, Input } from "@/components/ui";
import { ApiError, post } from "@/lib/api";
import { tr, useT } from "@/i18n";

export function ForgotPasswordPage() {
  const { t } = useT();
  const navigate = useNavigate();
  const [email, setEmail] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await post("/auth/password/forgot", { email });
      navigate(`/reset-password?email=${encodeURIComponent(email)}`);
    } catch (err) {
      setError(err instanceof ApiError ? err : new ApiError(0, "network", tr("common.networkError")));
    } finally {
      setBusy(false);
    }
  }

  return (
    <AuthLayout>
      <span className="mb-6 grid size-14 place-items-center rounded-2xl bg-amber-50 text-amber-600 ring-1 ring-amber-100"><KeyRound className="size-7" /></span>
      <AuthHeading title={t("auth.forgot.title")} subtitle={t("auth.forgot.subtitle")} />
      {error && !error.fields ? <div className="mb-5"><Alert tone="red">{error.message}</Alert></div> : null}
      <form onSubmit={submit} className="space-y-5" noValidate>
        <Field label={t("auth.email")} htmlFor="email" error={error?.fields?.email}>
          <div className="relative">
            <Mail className="pointer-events-none absolute top-1/2 left-3.5 size-4 -translate-y-1/2 text-slate-400" />
            <Input id="email" type="email" autoFocus autoComplete="username" value={email} onChange={(e) => setEmail(e.target.value)} placeholder={t("auth.emailPlaceholder")} className="h-12 pl-10" invalid={!!error?.fields?.email} />
          </div>
        </Field>
        <Button type="submit" size="lg" loading={busy} className="w-full">{t("auth.forgot.send")}</Button>
      </form>
      <Link to="/login" className="mt-6 inline-flex items-center gap-1.5 text-sm font-medium text-slate-500 hover:text-slate-800"><ArrowLeft className="size-4" /> {t("auth.forgot.back")}</Link>
    </AuthLayout>
  );
}
