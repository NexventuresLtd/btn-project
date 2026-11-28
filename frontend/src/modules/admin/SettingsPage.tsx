import { useEffect, useState, type FormEvent } from "react";
import { Building, FileText, Lock, ShieldCheck } from "lucide-react";
import { Alert, Button, Card, CardBody, CardHeader, ErrorState, Field, Input, PageHeader, PageLoader, Textarea } from "@/components/ui";
import { useT, type T } from "@/i18n";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { formatRWF } from "@/lib/format";
import { useAuth } from "@/auth/AuthProvider";
import { SystemResetCard } from "@/modules/superadmin/SystemResetCard";
import { ADMIN_KEY, LOCKOUT_BOUNDS, saveSettings, useSettings, type CompanySettings, type Settings } from "./api";

interface FormState {
  company: CompanySettings;
  approvals: { pettyCashApprovalLimit: string; maxDiscountPercent: string };
  security: { lockoutFailures: string; lockoutMinutes: string };
}

const toForm = (s: Settings): FormState => ({
  company: { ...s.company },
  approvals: { pettyCashApprovalLimit: String(s.approvals.pettyCashApprovalLimit), maxDiscountPercent: String(s.approvals.maxDiscountPercent) },
  security: { lockoutFailures: String(s.security?.lockoutFailures ?? 5), lockoutMinutes: String(s.security?.lockoutMinutes ?? 15) },
});

const inRange = (v: string, [lo, hi]: readonly [number, number]) => {
  const n = Number(v);
  return v.trim() !== "" && Number.isInteger(n) && n >= lo && n <= hi;
};

const COMPANY_FIELDS: { key: keyof CompanySettings; hint?: boolean; type?: string; wide?: boolean; required?: boolean; ph?: boolean }[] = [
  { key: "name", required: true, hint: true },
  { key: "legalName", required: true, hint: true },
  { key: "address", wide: true, ph: true },
  { key: "phone", type: "tel", ph: true },
  { key: "email", type: "email", ph: true },
  { key: "tin", hint: true, ph: true },
  { key: "website", ph: true },
];

function validate(f: FormState, t: T): Record<string, string> {
  const e: Record<string, string> = {};
  if (!f.company.name.trim()) e["company.name"] = t("common.required");
  if (!f.company.legalName.trim()) e["company.legalName"] = t("common.required");
  if (f.company.email.trim() && !/^\S+@\S+\.\S+$/.test(f.company.email.trim())) e["company.email"] = t("admin.md.err.email");
  if (f.company.tin.trim() && !/^\d{9}$/.test(f.company.tin.replace(/\s+/g, ""))) e["company.tin"] = t("admin.settings.err.tin");
  const limit = Number(f.approvals.pettyCashApprovalLimit);
  if (f.approvals.pettyCashApprovalLimit.trim() === "" || !Number.isInteger(limit) || limit < 0) e["approvals.pettyCashApprovalLimit"] = t("admin.settings.err.limit");
  const pct = Number(f.approvals.maxDiscountPercent);
  if (f.approvals.maxDiscountPercent.trim() === "" || !Number.isFinite(pct) || pct < 0 || pct > 100) e["approvals.maxDiscountPercent"] = t("admin.settings.err.pct");
  const [fMin, fMax] = LOCKOUT_BOUNDS.failures;
  const [mMin, mMax] = LOCKOUT_BOUNDS.minutes;
  if (!inRange(f.security.lockoutFailures, LOCKOUT_BOUNDS.failures)) e["security.lockoutFailures"] = t("lockout.err.failures", { min: fMin, max: fMax });
  if (!inRange(f.security.lockoutMinutes, LOCKOUT_BOUNDS.minutes)) e["security.lockoutMinutes"] = t("lockout.err.minutes", { min: mMin, max: mMax });
  return e;
}

export function SettingsPage() {
  const { t, tx } = useT();
  const settings = useSettings();
  const { user } = useAuth();
  const canLockout = user?.role === "SUPER_ADMIN"; // AUTH-05: the lockout policy (and the system reset) are the Super Administrator's
  const [form, setForm] = useState<FormState | null>(null);
  const [touched, setTouched] = useState(false);
  useEffect(() => {
    if (settings.data) setForm(toForm(settings.data));
  }, [settings.data]);

  const save = useMutate(saveSettings, {
    success: t("admin.settings.saved"),
    invalidate: [[...ADMIN_KEY, "settings"], ["settings"]],
    onSuccess: (s) => {
      setForm(toForm(s));
      setTouched(false);
    },
  });

  if (settings.isError) return (<><Header /><Card><ErrorState error={settings.error} retry={() => settings.refetch()} /></Card></>);
  if (!form || !settings.data) return (<><Header /><PageLoader /></>);

  const errors = validate(form, t);
  const err = (k: string) => (touched ? errors[k] : undefined) ?? fieldError(save.error, k);
  const dirty = JSON.stringify(form) !== JSON.stringify(toForm(settings.data));
  const setCompany = (k: keyof CompanySettings, v: string) => setForm((f) => f && { ...f, company: { ...f.company, [k]: v } });
  const setApproval = (k: keyof FormState["approvals"], v: string) => setForm((f) => f && { ...f, approvals: { ...f.approvals, [k]: v } });
  const setSecurity = (k: keyof FormState["security"], v: string) => setForm((f) => f && { ...f, security: { ...f.security, [k]: v } });

  function submit(e: FormEvent) {
    e.preventDefault();
    setTouched(true);
    if (!form || Object.keys(errors).length) return;
    save.mutate({
      company: Object.fromEntries(Object.entries(form.company).map(([k, v]) => [k, k === "tin" ? v.replace(/\s+/g, "") : v.trim()])) as unknown as CompanySettings,
      approvals: { pettyCashApprovalLimit: Number(form.approvals.pettyCashApprovalLimit), maxDiscountPercent: Number(form.approvals.maxDiscountPercent) },
      ...(canLockout ? { security: { lockoutFailures: Number(form.security.lockoutFailures), lockoutMinutes: Number(form.security.lockoutMinutes) } } : {}),
    });
  }

  const limit = Number(form.approvals.pettyCashApprovalLimit) || 0;
  const pct = Number(form.approvals.maxDiscountPercent) || 0;
  return (
    <>
      <Header />
      <form onSubmit={submit} noValidate className="space-y-5 pb-24">
        <div className="grid gap-5 xl:grid-cols-[1fr_340px]">
          <Card>
            <CardHeader title={<span className="flex items-center gap-2"><Building className="size-4 text-navy-600" />{t("admin.settings.company")}</span>} description={t("admin.settings.companyDesc")} />
            <CardBody className="grid gap-4 sm:grid-cols-2">
              {COMPANY_FIELDS.map((f) => (
                <Field key={f.key} label={tx(`admin.settings.field.${f.key}`)} required={f.required} hint={f.hint ? tx(`admin.settings.hint.${f.key}`) : undefined} error={err(`company.${f.key}`)} htmlFor={`s-${f.key}`} className={f.wide ? "sm:col-span-2" : undefined}>
                  {f.key === "address" ? (
                    <Textarea id={`s-${f.key}`} rows={2} value={form.company[f.key]} onChange={(e) => setCompany(f.key, e.target.value)} placeholder={f.ph ? tx(`admin.settings.ph.${f.key}`) : undefined} />
                  ) : (
                    <Input id={`s-${f.key}`} type={f.type ?? "text"} value={form.company[f.key]} onChange={(e) => setCompany(f.key, e.target.value)} placeholder={f.ph ? tx(`admin.settings.ph.${f.key}`) : undefined} invalid={!!err(`company.${f.key}`)} />
                  )}
                </Field>
              ))}
            </CardBody>
          </Card>
          <Card className="h-fit">
            <CardHeader title={<span className="flex items-center gap-2"><FileText className="size-4 text-navy-600" />{t("admin.settings.preview")}</span>} description={t("admin.settings.previewDesc")} />
            <CardBody>
              <div className="rounded-xl border border-line p-4">
                <div className="flex items-start justify-between gap-3 border-b-2 border-navy-800 pb-3">
                  <div className="min-w-0">
                    <p className="truncate text-base font-bold text-navy-900">{form.company.name || t("admin.settings.field.name")}</p>
                    <p className="truncate text-[11px] text-slate-500">{form.company.legalName || t("admin.settings.field.legalName")}</p>
                  </div>
                  <span className="mt-1 h-2 w-10 shrink-0 rounded-full bg-accent" />
                </div>
                <div className="mt-3 space-y-0.5 text-[11px] text-slate-500">
                  {[form.company.address, [form.company.phone, form.company.email].filter(Boolean).join(" · "), [form.company.tin && `TIN ${form.company.tin}`, form.company.website].filter(Boolean).join(" · ")]
                    .filter(Boolean)
                    .map((l, i) => <p key={i} className="truncate">{l}</p>)}
                </div>
              </div>
            </CardBody>
          </Card>
        </div>

        <Card>
          <CardHeader title={<span className="flex items-center gap-2"><ShieldCheck className="size-4 text-navy-600" />{t("admin.settings.rules")}</span>} description={t("admin.settings.rulesDesc")} />
          <CardBody className="grid gap-5 md:grid-cols-2">
            <Field
              label={t("admin.settings.limitLabel")}
              required
              htmlFor="s-limit"
              error={err("approvals.pettyCashApprovalLimit")}
              hint={
                limit > 0
                  ? t("admin.settings.limitHint", { amount: formatRWF(limit) })
                  : t("admin.settings.limitHintZero")
              }
            >
              <Input id="s-limit" type="number" inputMode="numeric" min={0} step={1000} className="tabular" value={form.approvals.pettyCashApprovalLimit} onChange={(e) => setApproval("pettyCashApprovalLimit", e.target.value)} invalid={!!err("approvals.pettyCashApprovalLimit")} />
            </Field>
            <Field
              label={t("admin.settings.discountLabel")}
              required
              htmlFor="s-discount"
              error={err("approvals.maxDiscountPercent")}
              hint={t("admin.settings.discountHint", { pct, price: formatRWF(Math.ceil(1500 * (1 - pct / 100))) })}
            >
              <Input id="s-discount" type="number" inputMode="decimal" min={0} max={100} step="0.5" className="tabular" value={form.approvals.maxDiscountPercent} onChange={(e) => setApproval("maxDiscountPercent", e.target.value)} invalid={!!err("approvals.maxDiscountPercent")} />
            </Field>
            <div className="md:col-span-2">
              <Alert tone="blue">{t("admin.settings.note")}</Alert>
            </div>
          </CardBody>
        </Card>

        <Card>
          <CardHeader title={<span className="flex items-center gap-2"><Lock className="size-4 text-navy-600" />{t("lockout.title")}</span>} description={t("lockout.description")} />
          <CardBody className="grid gap-5 md:grid-cols-2">
            <Field label={t("lockout.failuresLabel")} required htmlFor="s-lock-failures" error={err("security.lockoutFailures")} hint={t("lockout.failuresHint", { n: form.security.lockoutFailures || "…" })}>
              <Input id="s-lock-failures" type="number" inputMode="numeric" min={LOCKOUT_BOUNDS.failures[0]} max={LOCKOUT_BOUNDS.failures[1]} step={1} className="tabular" value={form.security.lockoutFailures} onChange={(e) => setSecurity("lockoutFailures", e.target.value)} invalid={!!err("security.lockoutFailures")} disabled={!canLockout} />
            </Field>
            <Field label={t("lockout.minutesLabel")} required htmlFor="s-lock-minutes" error={err("security.lockoutMinutes")} hint={t("lockout.minutesHint", { n: form.security.lockoutMinutes || "…" })}>
              <Input id="s-lock-minutes" type="number" inputMode="numeric" min={LOCKOUT_BOUNDS.minutes[0]} max={LOCKOUT_BOUNDS.minutes[1]} step={1} className="tabular" value={form.security.lockoutMinutes} onChange={(e) => setSecurity("lockoutMinutes", e.target.value)} invalid={!!err("security.lockoutMinutes")} disabled={!canLockout} />
            </Field>
            {!canLockout && (
              <div className="md:col-span-2">
                <Alert tone="blue">{t("lockout.readOnly")}</Alert>
              </div>
            )}
          </CardBody>
        </Card>

        <div className="sticky bottom-4 z-10 flex flex-wrap items-center justify-end gap-3 rounded-2xl border border-line bg-white/95 px-5 py-3 shadow-[var(--shadow-card)] backdrop-blur">
          <p className="mr-auto text-sm text-slate-500">{dirty ? t("admin.settings.unsaved") : t("admin.settings.allSaved")}</p>
          <Button variant="secondary" disabled={!dirty || save.isPending} onClick={() => { setForm(toForm(settings.data!)); setTouched(false); }}>{t("admin.settings.discard")}</Button>
          <Button type="submit" disabled={!dirty} loading={save.isPending}>{t("admin.settings.save")}</Button>
        </div>
      </form>
      {canLockout ? <SystemResetCard /> : null}
    </>
  );
}

function Header() {
  const { t } = useT();
  return <PageHeader title={t("admin.settings.title")} description={t("admin.settings.description")} breadcrumbs={[{ label: t("admin.breadcrumb") }, { label: t("admin.settings.title") }]} />;
}
