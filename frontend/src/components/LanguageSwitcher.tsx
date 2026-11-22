import { useEffect, useRef, useState } from "react";
import { Check } from "lucide-react";
import { LANGS, LANG_NAMES, setLang, useLang, useT, type Lang } from "@/i18n";
import { put } from "@/lib/api";
import { useAuth } from "@/auth/AuthProvider";
import { cn } from "@/lib/cn";
import { Flag } from "./Flag";

/** Globe button + menu. Persists to localStorage always, and to the user's profile when signed in. */
export function LanguageSwitcher({ variant = "light", className }: { variant?: "light" | "dark"; className?: string }) {
  const lang = useLang();
  const { t } = useT();
  const { user } = useAuth();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent) => ref.current && !ref.current.contains(e.target as Node) && setOpen(false);
    const esc = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    document.addEventListener("mousedown", close);
    document.addEventListener("keydown", esc);
    return () => {
      document.removeEventListener("mousedown", close);
      document.removeEventListener("keydown", esc);
    };
  }, [open]);

  function choose(l: Lang) {
    setOpen(false);
    setLang(l);
    if (user && user.language !== l) void put("/auth/language", { language: l }).catch(() => {});
  }

  return (
    <div className={cn("relative", className)} ref={ref} data-tour="language-switcher">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={t("layout.languageSwitch")}
        className={cn(
          "flex h-9 items-center gap-1.5 rounded-xl px-2.5 text-[13px] font-medium transition",
          variant === "dark" ? "text-white/90 hover:bg-white/10" : "text-slate-600 hover:bg-slate-100",
        )}
      >
        <Flag lang={lang} className="h-[14px] w-5" />
        <span className="max-w-[7rem] truncate">{LANG_NAMES[lang]}</span>
      </button>
      {open ? (
        <ul role="menu" className="absolute right-0 z-50 mt-2 w-44 overflow-hidden rounded-xl border border-line bg-white py-1 text-slate-700 shadow-xl">
          {LANGS.map((l) => (
            <li key={l} role="none">
              <button role="menuitemradio" aria-checked={l === lang} onClick={() => choose(l)} className="flex w-full items-center gap-2.5 px-3.5 py-2 text-left text-sm hover:bg-slate-50" lang={l}>
                <Flag lang={l} className="h-[14px] w-5 shrink-0" />
                <span className="flex-1">{LANG_NAMES[l]}</span>
                {l === lang ? <Check className="size-4 text-navy-600" /> : null}
              </button>
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}
