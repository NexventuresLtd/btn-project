import { useEffect, useMemo, useRef, useState, type DragEvent } from "react";
import { AlertTriangle, ImageUp, RotateCcw, Trash2, UploadCloud } from "lucide-react";
import { toast } from "sonner";
import { Alert, Button, Card, CardBody, CardHeader, Field, Input, PageHeader, Select, Tabs, Textarea } from "@/components/ui";
import { DEFAULT_BRANDING, FONT_CHOICES, applyBrandingToDom, ensureFont, fontStack, refreshBranding, setBranding, useBranding, type Branding, type LocalText } from "@/branding";
import { contrast, isHex } from "@/branding/colors";
import { LANGS, LANG_NAMES, useT, type Lang } from "@/i18n";
import { ApiError } from "@/lib/api";
import { removeBrandingAsset, saveBranding, uploadBrandingAsset, type AssetKind } from "./api";

const MAX_BYTES = 1024 * 1024;
const TYPES = ["image/png", "image/jpeg", "image/svg+xml", "image/webp", "image/x-icon", "image/vnd.microsoft.icon"];

type Draft = Omit<Branding, "version">;
const toDraft = (b: Branding): Draft => { const { version, ...d } = b; void version; return d; };

function ColorField({ id, label, value, onChange }: { id: string; label: string; value: string; onChange: (v: string) => void }) {
  return (
    <Field label={label} htmlFor={id}>
      <div className="flex items-center gap-2">
        <input type="color" id={id} value={isHex(value) ? value : "#000000"} onChange={(e) => onChange(e.target.value)} className="h-10 w-12 shrink-0 cursor-pointer rounded-lg border border-line bg-white p-1" />
        <Input value={value} onChange={(e) => onChange(e.target.value.startsWith("#") ? e.target.value : `#${e.target.value}`)} maxLength={7} className="font-mono uppercase" invalid={!isHex(value)} aria-label={label} />
      </div>
    </Field>
  );
}

function AssetDrop({ kind, label, hint, url, defaultUrl, dark, onChanged }: { kind: AssetKind; label: string; hint: string; url: string; defaultUrl: string; dark?: boolean; onChanged: () => Promise<void> }) {
  const { t } = useT();
  const [busy, setBusy] = useState(false);
  const [over, setOver] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const input = useRef<HTMLInputElement>(null);

  async function handle(file: File | undefined) {
    if (!file) return;
    setError(null);
    if (!TYPES.includes(file.type)) return setError(t("superadmin.branding.badType"));
    if (file.size > MAX_BYTES) return setError(t("superadmin.branding.tooBig"));
    setBusy(true);
    try {
      await uploadBrandingAsset(kind, file);
      await onChanged();
      toast.success(t("superadmin.branding.uploaded"));
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t("common.somethingWrong"));
    } finally {
      setBusy(false);
    }
  }
  async function remove() {
    setBusy(true);
    try {
      await removeBrandingAsset(kind);
      await onChanged();
      toast.success(t("superadmin.branding.removed"));
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t("common.somethingWrong"));
    } finally {
      setBusy(false);
    }
  }
  const custom = url !== defaultUrl && !url.includes("/default-");
  return (
    <div>
      <p className="mb-1.5 text-[13px] font-medium text-slate-700">{label}</p>
      <div
        onDragOver={(e: DragEvent) => { e.preventDefault(); setOver(true); }}
        onDragLeave={() => setOver(false)}
        onDrop={(e: DragEvent) => { e.preventDefault(); setOver(false); void handle(e.dataTransfer.files[0]); }}
        className={`flex flex-col items-center gap-3 rounded-xl border-2 border-dashed p-4 text-center transition ${over ? "border-navy-400 bg-navy-50" : "border-line bg-slate-50/50"}`}
      >
        <div className={`grid h-16 w-full place-items-center rounded-lg ${dark ? "bg-navy-800" : "bg-white ring-1 ring-line"}`}>
          <img src={url} alt={label} className="max-h-12 max-w-[85%] object-contain" />
        </div>
        <p className="text-xs text-slate-500">{hint}</p>
        <div className="flex flex-wrap justify-center gap-2">
          <Button size="sm" variant="secondary" loading={busy} icon={<UploadCloud className="size-3.5" />} onClick={() => input.current?.click()}>{t("superadmin.branding.chooseFile")}</Button>
          {custom ? <Button size="sm" variant="ghost" disabled={busy} icon={<Trash2 className="size-3.5" />} onClick={() => void remove()} className="text-red-600">{t("common.remove")}</Button> : null}
        </div>
        <p className="text-[11px] text-slate-400">{t("superadmin.branding.dropHint")}</p>
        <input ref={input} type="file" accept={TYPES.join(",")} hidden onChange={(e) => { void handle(e.target.files?.[0]); e.target.value = ""; }} />
      </div>
      {error ? <p role="alert" className="mt-1.5 text-xs text-red-600">{error}</p> : null}
    </div>
  );
}

function Preview({ d }: { d: Draft }) {
  const { t, lang } = useT();
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => { if (ref.current) applyBrandingToDom({ ...d, version: 0 }, ref.current); }, [d]);
  const headline = d.loginHeadline[lang] || `${t("layout.panel.headline")} ${t("layout.panel.headlineAccent")}`;
  const sub = d.loginSubtitle[lang] || t("layout.panel.body");
  return (
    <div ref={ref} style={{ fontFamily: fontStack(d.fontFamily), fontSize: `${d.baseFontSize}px` }} className="overflow-hidden rounded-2xl border border-line bg-white shadow-sm" aria-label={t("superadmin.branding.preview")}>
      <div className="grid grid-cols-[minmax(0,1.1fr)_minmax(0,1fr)]">
        <div className="space-y-[1em] p-[1.25em] text-white" style={{ background: "linear-gradient(135deg,var(--brand-950),var(--brand-800))" }}>
          <img src={d.logoDarkUrl} alt="" className="h-[2em] w-auto max-w-full object-contain" />
          <p className="text-[1.25em] leading-tight font-bold">{headline}</p>
          <p className="text-[0.75em] leading-relaxed opacity-80">{sub}</p>
          <span className="inline-block rounded-full px-[0.7em] py-[0.15em] text-[0.7em] font-semibold" style={{ background: "var(--brand-accent)", color: "#1b1204" }}>{d.name}</span>
        </div>
        <div className="space-y-[0.7em] p-[1.25em]">
          <img src={d.logoLightUrl} alt="" className="h-[1.8em] w-auto max-w-full object-contain" />
          <p className="text-[1.1em] font-bold text-slate-900">{t("auth.login.title")}</p>
          <div className="h-[2.2em] rounded-lg border border-slate-200 bg-white" />
          <button type="button" tabIndex={-1} className="w-full rounded-lg py-[0.6em] text-[0.85em] font-semibold text-white" style={{ background: "var(--brand-800)" }}>{t("auth.signIn")}</button>
          <div className="flex gap-[0.5em]">
            <span className="rounded-full px-[0.6em] py-[0.15em] text-[0.7em] font-semibold" style={{ background: "var(--brand-50)", color: "var(--brand-700)" }}>BTN-1</span>
            <span className="rounded-full px-[0.6em] py-[0.15em] text-[0.7em] font-semibold" style={{ background: "var(--brand-accent)", color: "#1b1204" }}>RWF</span>
          </div>
        </div>
      </div>
      <div className="flex gap-[0.4em] border-t border-line bg-slate-50 p-[0.8em]">
        {[50, 100, 200, 300, 400, 500, 600, 700, 800, 900, 950].map((s) => <span key={s} className="h-[1.2em] flex-1 rounded" style={{ background: `var(--brand-${s})` }} title={`navy-${s}`} />)}
      </div>
    </div>
  );
}

export function BrandingPage() {
  const { t } = useT();
  const live = useBranding();
  const [d, setD] = useState<Draft>(() => toDraft(live));
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  const [tab, setTab] = useState<Lang>("en");
  const [customFont, setCustomFont] = useState(() => !(FONT_CHOICES as readonly string[]).includes(live.fontFamily));
  const loaded = useRef(false);

  // pull the freshest server copy once
  useEffect(() => {
    void refreshBranding().then((b) => { if (!loaded.current) setD((cur) => (dirty ? cur : toDraft(b))); loaded.current = true; });
  }, []); // eslint-disable-line react-hooks/exhaustive-deps
  // logos follow the store (uploads update them immediately, independent of the draft)
  useEffect(() => setD((cur) => ({ ...cur, logoLightUrl: live.logoLightUrl, logoDarkUrl: live.logoDarkUrl, faviconUrl: live.faviconUrl })), [live.logoLightUrl, live.logoDarkUrl, live.faviconUrl]);
  useEffect(() => { ensureFont(d.fontFamily); }, [d.fontFamily]);

  const edit = (patch: Partial<Draft>) => { setD((c) => ({ ...c, ...patch })); setDirty(true); };
  const editText = (field: "loginHeadline" | "loginSubtitle" | "footerText", lang: Lang, v: string) => edit({ [field]: { ...d[field], [lang]: v } } as Partial<Draft>);

  const warnings = useMemo(() => {
    const w: string[] = [];
    if (isHex(d.primaryColor) && contrast(d.primaryColor, "#ffffff") < 4.5) w.push(t("superadmin.branding.warnPrimary", { ratio: contrast(d.primaryColor, "#ffffff").toFixed(1) }));
    if (isHex(d.primaryColor) && isHex(d.accentColor) && contrast(d.primaryColor, d.accentColor) < 3) w.push(t("superadmin.branding.warnAccent", { ratio: contrast(d.primaryColor, d.accentColor).toFixed(1) }));
    return w;
  }, [d.primaryColor, d.accentColor, t]);

  async function save() {
    if (!isHex(d.primaryColor) || !isHex(d.accentColor)) return toast.error(t("superadmin.branding.badColor"));
    if (!d.name.trim()) return toast.error(t("superadmin.branding.nameRequired"));
    setSaving(true);
    try {
      const { logoLightUrl, logoDarkUrl, faviconUrl, ...input } = d;
      void logoLightUrl; void logoDarkUrl; void faviconUrl;
      const res = await saveBranding(input);
      if (res && typeof res === "object" && "primaryColor" in res) setBranding(res);
      else await refreshBranding();
      setDirty(false);
      toast.success(t("superadmin.branding.saved"));
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t("common.somethingWrong"));
    } finally {
      setSaving(false);
    }
  }

  function reset() {
    const { logoLightUrl, logoDarkUrl, faviconUrl } = d;
    setD({ ...toDraft(DEFAULT_BRANDING), logoLightUrl, logoDarkUrl, faviconUrl });
    setCustomFont(false);
    setDirty(true);
  }

  const fontValue = customFont ? "__custom" : d.fontFamily;
  const textFields: { field: "loginHeadline" | "loginSubtitle" | "footerText"; label: string; multiline?: boolean }[] = [
    { field: "loginHeadline", label: t("superadmin.branding.headline") },
    { field: "loginSubtitle", label: t("superadmin.branding.subtitle"), multiline: true },
    { field: "footerText", label: t("superadmin.branding.footer") },
  ];

  return (
    <div>
      <PageHeader
        title={t("superadmin.branding.title")}
        description={t("superadmin.branding.description")}
        actions={<>
          <Button variant="secondary" icon={<RotateCcw className="size-4" />} onClick={reset}>{t("superadmin.branding.reset")}</Button>
          <Button loading={saving} disabled={!dirty} onClick={() => void save()}>{t("common.saveChanges")}</Button>
        </>}
      />
      <div className="grid items-start gap-6 xl:grid-cols-[minmax(0,1fr)_minmax(0,26rem)]">
        <div className="space-y-6">
          <Card>
            <CardHeader title={t("superadmin.branding.identity")} />
            <CardBody className="grid gap-4 sm:grid-cols-2">
              <Field label={t("superadmin.branding.name")} htmlFor="b-name"><Input id="b-name" value={d.name} maxLength={80} onChange={(e) => edit({ name: e.target.value })} /></Field>
              <Field label={t("superadmin.branding.legalName")} htmlFor="b-legal"><Input id="b-legal" value={d.legalName} maxLength={120} onChange={(e) => edit({ legalName: e.target.value })} /></Field>
              <Field label={t("superadmin.branding.defaultLanguage")} htmlFor="b-lang" hint={t("superadmin.branding.defaultLanguageHint")}>
                <Select id="b-lang" value={d.defaultLanguage} onChange={(e) => edit({ defaultLanguage: e.target.value as Lang })}>
                  {LANGS.map((l) => <option key={l} value={l}>{LANG_NAMES[l]}</option>)}
                </Select>
              </Field>
            </CardBody>
          </Card>

          <Card>
            <CardHeader title={t("superadmin.branding.colours")} description={t("superadmin.branding.coloursHint")} />
            <CardBody className="space-y-4">
              <div className="grid gap-4 sm:grid-cols-2">
                <ColorField id="b-primary" label={t("superadmin.branding.primary")} value={d.primaryColor} onChange={(v) => edit({ primaryColor: v })} />
                <ColorField id="b-accent" label={t("superadmin.branding.accent")} value={d.accentColor} onChange={(v) => edit({ accentColor: v })} />
              </div>
              {warnings.map((w) => (
                <Alert key={w} tone="amber"><span className="flex items-start gap-2"><AlertTriangle className="mt-0.5 size-4 shrink-0" />{w}</span></Alert>
              ))}
            </CardBody>
          </Card>

          <Card>
            <CardHeader title={t("superadmin.branding.typography")} />
            <CardBody className="space-y-4">
              <div className="grid gap-4 sm:grid-cols-2">
                <Field label={t("superadmin.branding.font")} htmlFor="b-font">
                  <Select id="b-font" value={fontValue} onChange={(e) => { if (e.target.value === "__custom") { setCustomFont(true); edit({ fontFamily: "" }); } else { setCustomFont(false); edit({ fontFamily: e.target.value }); } }}>
                    {FONT_CHOICES.map((f) => <option key={f} value={f}>{f}</option>)}
                    <option value="__custom">{t("superadmin.branding.fontCustom")}</option>
                  </Select>
                </Field>
                {customFont ? (
                  <Field label={t("superadmin.branding.fontCustomName")} hint={t("superadmin.branding.fontCustomHint")} htmlFor="b-font2">
                    <Input id="b-font2" value={d.fontFamily} onChange={(e) => edit({ fontFamily: e.target.value })} placeholder="Merriweather" />
                  </Field>
                ) : null}
              </div>
              <Field label={t("superadmin.branding.baseSize", { size: d.baseFontSize })} htmlFor="b-size">
                <div className="flex items-center gap-3">
                  <span className="text-xs text-slate-400">12</span>
                  <input id="b-size" type="range" min={12} max={20} step={1} value={d.baseFontSize} onChange={(e) => edit({ baseFontSize: Number(e.target.value) })} className="h-2 flex-1 accent-[var(--brand-800)]" />
                  <span className="text-xs text-slate-400">20</span>
                </div>
              </Field>
              <p className="rounded-xl bg-slate-50 px-4 py-3 text-slate-700" style={{ fontFamily: fontStack(d.fontFamily), fontSize: d.baseFontSize }}>{t("superadmin.branding.sample")}</p>
            </CardBody>
          </Card>

          <Card>
            <CardHeader title={t("superadmin.branding.logos")} description={t("superadmin.branding.logosHint")} />
            <CardBody className="grid gap-4 md:grid-cols-3">
              <AssetDrop kind="light" label={t("superadmin.branding.logoLight")} hint={t("superadmin.branding.logoLightHint")} url={d.logoLightUrl} defaultUrl={DEFAULT_BRANDING.logoLightUrl} onChanged={async () => { await refreshBranding(); }} />
              <AssetDrop kind="dark" dark label={t("superadmin.branding.logoDark")} hint={t("superadmin.branding.logoDarkHint")} url={d.logoDarkUrl} defaultUrl={DEFAULT_BRANDING.logoDarkUrl} onChanged={async () => { await refreshBranding(); }} />
              <AssetDrop kind="favicon" label={t("superadmin.branding.favicon")} hint={t("superadmin.branding.faviconHint")} url={d.faviconUrl} defaultUrl={DEFAULT_BRANDING.faviconUrl} onChanged={async () => { await refreshBranding(); }} />
            </CardBody>
          </Card>

          <Card>
            <CardHeader title={t("superadmin.branding.loginText")} description={t("superadmin.branding.loginTextHint")} />
            <CardBody className="space-y-4">
              <Tabs tabs={LANGS.map((l) => ({ value: l, label: LANG_NAMES[l] }))} value={tab} onChange={setTab} />
              {textFields.map(({ field, label, multiline }) => {
                const val = (d[field] as Partial<LocalText>)[tab] ?? "";
                return (
                  <Field key={field + tab} label={label} htmlFor={`b-${field}`} hint={t("superadmin.branding.emptyHint")}>
                    {multiline
                      ? <Textarea id={`b-${field}`} lang={tab} value={val} maxLength={300} onChange={(e) => editText(field, tab, e.target.value)} />
                      : <Input id={`b-${field}`} lang={tab} value={val} maxLength={160} onChange={(e) => editText(field, tab, e.target.value)} />}
                  </Field>
                );
              })}
            </CardBody>
          </Card>
        </div>

        <div className="xl:sticky xl:top-24">
          <Card>
            <CardHeader title={t("superadmin.branding.preview")} description={t("superadmin.branding.previewHint")} />
            <CardBody><Preview d={d} /></CardBody>
          </Card>
          <p className="mt-3 flex items-center gap-2 text-xs text-slate-400"><ImageUp className="size-3.5" /> {t("superadmin.branding.assetRules")}</p>
        </div>
      </div>
    </div>
  );
}
