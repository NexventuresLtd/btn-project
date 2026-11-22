import type { ReactNode } from "react";
import { Boxes, ClipboardCheck, Factory, ShieldCheck, Wallet } from "lucide-react";
import { useBranding, localText } from "@/branding";
import { useT } from "@/i18n";
import { LanguageSwitcher } from "@/components/LanguageSwitcher";

/** Split-screen shell for every sign-in step: brand photo panel + form panel. Consumes Super Admin branding. */
export function AuthLayout({ children }: { children: ReactNode }) {
  const { t, lang } = useT();
  const b = useBranding();
  const highlights = [
    { icon: Factory, label: t("layout.panel.h1") },
    { icon: Boxes, label: t("layout.panel.h2") },
    { icon: Wallet, label: t("layout.panel.h3") },
    { icon: ClipboardCheck, label: t("layout.panel.h4") },
  ];
  const headline = localText(b.loginHeadline, lang);
  const subtitle = localText(b.loginSubtitle, lang);
  const footer = localText(b.footerText, lang);
  return (
    <div className="flex min-h-svh bg-white">
      <aside className="relative hidden w-[46%] max-w-[760px] overflow-hidden lg:flex">
        <img src="/brand/yard.jpg" alt="" className="absolute inset-0 h-full w-full object-cover" />
        <div className="absolute inset-0 bg-gradient-to-br from-navy-950/95 via-navy-900/88 to-navy-800/75" />
        <div className="absolute -right-24 -bottom-24 size-80 rounded-full border-[40px] border-accent/15" />
        <div className="relative z-10 flex w-full flex-col justify-between p-12 xl:p-14">
          <img src={b.logoDarkUrl} alt={b.name} className="h-12 w-auto max-w-[16rem] self-start object-contain" />
          <div className="max-w-md">
            <span className="mb-5 inline-flex items-center gap-2 rounded-full bg-white/10 px-3 py-1 text-xs font-semibold tracking-wide text-amber-300 ring-1 ring-white/15 backdrop-blur">
              <span className="size-1.5 shrink-0 rounded-full bg-accent" /> {t("layout.panel.badge")}
            </span>
            <h1 className="text-[40px] leading-[1.1] font-bold tracking-tight text-white">
              {headline ? headline : <>{t("layout.panel.headline")} <span className="text-accent">{t("layout.panel.headlineAccent")}</span></>}
            </h1>
            <p className="mt-4 text-[15px] leading-relaxed text-navy-100/85">{subtitle || t("layout.panel.body")}</p>
            <ul className="mt-9 space-y-3.5">
              {highlights.map(({ icon: Icon, label }) => (
                <li key={label} className="flex items-center gap-3 text-sm text-white/90">
                  <span className="grid size-8 shrink-0 place-items-center rounded-lg bg-white/10 ring-1 ring-white/15"><Icon className="size-4 text-amber-300" /></span>
                  {label}
                </li>
              ))}
            </ul>
          </div>
          <p className="text-xs text-navy-200/70">{footer || t("layout.panel.copyright", { year: new Date().getFullYear(), company: b.legalName })}</p>
        </div>
      </aside>

      <main className="flex flex-1 flex-col">
        <div className="flex justify-end px-4 pt-4 sm:px-6"><LanguageSwitcher /></div>
        <div className="flex flex-1 items-center justify-center px-6 py-8 sm:px-10">
          <div className="w-full max-w-[400px]">
            <img src={b.logoLightUrl} alt={b.name} className="mb-10 h-10 w-auto max-w-[14rem] object-contain lg:hidden" />
            {children}
          </div>
        </div>
        <footer className="flex items-center justify-center gap-2 px-6 pb-6 text-center text-xs text-slate-400">
          <ShieldCheck className="size-3.5 shrink-0" /> {t("layout.panel.footer")}
        </footer>
      </main>
    </div>
  );
}

export function AuthHeading({ title, subtitle }: { title: string; subtitle?: ReactNode }) {
  return (
    <div className="mb-8">
      <h2 className="text-[28px] leading-tight font-bold tracking-tight text-slate-900">{title}</h2>
      {subtitle ? <p className="mt-2 text-[15px] leading-relaxed text-slate-500">{subtitle}</p> : null}
    </div>
  );
}
