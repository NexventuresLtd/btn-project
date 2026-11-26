import { useCallback, useMemo, useSyncExternalStore } from "react";
import { en, type MessageKey } from "./en";
import { am } from "./am";
import { rw } from "./rw";
import { fr } from "./fr";

export type Lang = "en" | "am" | "rw" | "fr";
export const LANGS: readonly Lang[] = ["en", "am", "rw", "fr"];
export const LANG_NAMES: Record<Lang, string> = { en: "English", am: "አማርኛ", rw: "Kinyarwanda", fr: "Français" };
export type { MessageKey };

/** Keys without the plural suffix: t("sales.items", { count }) resolves sales.items_one / _other. */
type BaseKey<K> = K extends `${infer B}_one` | `${infer B}_other` ? B : K;
export type TKey = BaseKey<MessageKey>;
export type Vars = Record<string, string | number | null | undefined>;

export const CATALOGS: Record<Lang, Record<string, string>> = { en, am, rw, fr };
/** Intl locales: French as used in Rwanda (fr-RW; Intl falls back to fr when a runtime lacks it). */
const LOCALES: Record<Lang, string> = { en: "en-GB", am: "am-ET", rw: "rw-RW", fr: "fr-RW" };

const LS_KEY = "btn.lang";
export const isLang = (v: unknown): v is Lang => v === "en" || v === "am" || v === "rw" || v === "fr";

function readStored(): Lang | null {
  try {
    const v = localStorage.getItem(LS_KEY);
    return isLang(v) ? v : null;
  } catch {
    return null;
  }
}

/* ---- store: current language + super-admin overrides ------------------------------------ */
let current: Lang = readStored() ?? "en";
let overrides: Record<Lang, Record<string, string>> = { en: {}, am: {}, rw: {}, fr: {} };
let stamp = 0;
const listeners = new Set<() => void>();
const emit = () => {
  stamp++;
  listeners.forEach((l) => l());
};
const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => listeners.delete(l);
};

function applyHtmlLang() {
  if (typeof document !== "undefined") document.documentElement.lang = current;
}
applyHtmlLang();

export const getLang = () => current;
export const hasStoredLang = () => readStored() !== null;

/** Switch language locally (localStorage + <html lang>). Server persistence is done by the caller (see LanguageSwitcher). */
export function setLang(lang: Lang) {
  if (lang === current) return;
  current = lang;
  try {
    localStorage.setItem(LS_KEY, lang);
  } catch { /* storage blocked */ }
  applyHtmlLang();
  emit();
}

/** Use a default only when the visitor never chose a language (e.g. the branding default). */
export function setDefaultLang(lang: Lang) {
  if (!hasStoredLang()) {
    current = lang;
    applyHtmlLang();
    emit();
  }
}

export function setOverrides(lang: Lang, messages: Record<string, string>) {
  overrides = { ...overrides, [lang]: messages };
  emit();
}
export const getOverrides = (lang: Lang) => overrides[lang];

/* ---- lookup ------------------------------------------------------------------------------ */
function interpolate(s: string, vars?: Vars) {
  if (!vars) return s;
  return s.replace(/\{(\w+)\}/g, (m, k: string) => (vars[k] == null ? m : String(vars[k])));
}

function raw(lang: Lang, key: string): string | undefined {
  return overrides[lang][key] ?? CATALOGS[lang][key];
}

/** Does a (possibly dynamic) key exist in the English catalog? */
export const hasKey = (key: string) => key in en;

function translate(lang: Lang, key: string, vars?: Vars): string {
  let msg: string | undefined;
  if (vars && typeof vars.count === "number") {
    const rule = new Intl.PluralRules(LOCALES[lang]).select(vars.count);
    msg = raw(lang, `${key}_${rule}`) ?? raw(lang, `${key}_other`) ?? raw(lang, `${key}_one`);
    if (msg === undefined) msg = raw("en", `${key}_${rule}`) ?? raw("en", `${key}_other`);
  }
  msg ??= raw(lang, key) ?? raw("en", key) ?? key;
  return interpolate(msg, vars);
}

/** Non-hook translate for code outside React (toasts in callbacks, api errors). */
export const tr = (key: TKey, vars?: Vars) => translate(current, key, vars);
/** Dynamic key (enum labels etc.): falls back to `fallback` (default: the last key segment) when missing. */
export const trx = (key: string, vars?: Vars, fallback?: string) => (hasKey(key) || hasKey(`${key}_other`) ? translate(current, key, vars) : (fallback ?? key.split(".").pop() ?? key));

export function useLang(): Lang {
  return useSyncExternalStore(subscribe, getLang, getLang);
}

export interface T {
  (key: TKey, vars?: Vars): string;
}

/** `t` is typed (compile-time checked keys); `tx` takes dynamic keys such as `enums.role.${role}` with a fallback. */
export function useT() {
  const lang = useLang();
  const version = useSyncExternalStore(subscribe, () => stamp, () => stamp);
  const t = useCallback<T>((key, vars) => translate(lang, key, vars), [lang, version]); // eslint-disable-line react-hooks/exhaustive-deps
  const tx = useCallback((key: string, vars?: Vars, fallback?: string) => (hasKey(key) || hasKey(`${key}_other`) ? translate(lang, key, vars) : (fallback ?? key.split(".").pop() ?? key)), [lang, version]); // eslint-disable-line react-hooks/exhaustive-deps
  return useMemo(() => ({ t, tx, lang, locale: LOCALES[lang] }), [t, tx, lang]);
}

export const localeOf = (lang: Lang = current) => LOCALES[lang];

/** Label for an enum value from the `enums.<group>.<VALUE>` convention, falling back to a humanised value. */
export function enumLabel(group: string, value: string | null | undefined, tx: (k: string, v?: Vars, f?: string) => string): string {
  if (!value) return "—";
  return tx(`enums.${group}.${value}`, undefined, value.replace(/_/g, " ").toLowerCase().replace(/^\w/, (c) => c.toUpperCase()));
}
