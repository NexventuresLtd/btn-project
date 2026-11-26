import { useSyncExternalStore } from "react";
import { generateScale, darken, isHex, SHADES } from "./colors";
import { setDefaultLang, isLang, type Lang } from "@/i18n";

export type LocalText = { en: string; am: string; rw: string; fr: string };

export interface Branding {
  name: string;
  legalName: string;
  primaryColor: string;
  accentColor: string;
  fontFamily: string;
  baseFontSize: number;
  logoLightUrl: string;
  logoDarkUrl: string;
  faviconUrl: string;
  loginHeadline: Partial<LocalText>;
  loginSubtitle: Partial<LocalText>;
  footerText: Partial<LocalText>;
  defaultLanguage: Lang;
  version: string | number;
}

/** Built-in look, used until/unless the API answers (old backend → 404). Empty text maps fall back to the translated catalog. */
export const DEFAULT_BRANDING: Branding = {
  name: "BTN Manufacturing",
  legalName: "BTN Manufacturing Ltd",
  primaryColor: "#0c234b",
  accentColor: "#f59e0b",
  fontFamily: "Inter",
  baseFontSize: 16,
  logoLightUrl: "/brand/logo-on-light.png",
  logoDarkUrl: "/brand/logo-on-dark.png",
  faviconUrl: "/brand/favicon.png",
  loginHeadline: {},
  loginSubtitle: {},
  footerText: {},
  defaultLanguage: "en",
  version: 0,
};

export const FONT_CHOICES = ["Inter", "Poppins", "Roboto", "Open Sans", "Noto Sans", "Nunito", "Lato", "Source Sans 3"] as const;
const LS = "btn.branding";

function sanitize(raw: Partial<Branding> | null | undefined): Branding {
  const b = { ...DEFAULT_BRANDING, ...(raw ?? {}) } as Branding;
  for (const k of Object.keys(DEFAULT_BRANDING) as (keyof Branding)[]) {
    if ((b[k] === null || b[k] === "" || b[k] === undefined) && k !== "version") (b as unknown as Record<string, unknown>)[k] = DEFAULT_BRANDING[k];
  }
  if (!isHex(b.primaryColor)) b.primaryColor = DEFAULT_BRANDING.primaryColor;
  if (!isHex(b.accentColor)) b.accentColor = DEFAULT_BRANDING.accentColor;
  b.baseFontSize = Math.min(20, Math.max(12, Number(b.baseFontSize) || 16));
  if (!isLang(b.defaultLanguage)) b.defaultLanguage = "en";
  for (const k of ["loginHeadline", "loginSubtitle", "footerText"] as const) if (!b[k] || typeof b[k] !== "object") b[k] = {};
  return b;
}

let current: Branding = sanitize(null);
try {
  const cached = localStorage.getItem(LS);
  if (cached) current = sanitize(JSON.parse(cached));
} catch { /* ignore */ }

const listeners = new Set<() => void>();
const subscribe = (l: () => void) => (listeners.add(l), () => listeners.delete(l));
export const getBranding = () => current;
export function useBranding() {
  return useSyncExternalStore(subscribe, getBranding, getBranding);
}

const loadedFonts = new Set<string>();
export function fontStack(family: string) {
  const f = family.trim();
  const fallback = `"Noto Sans Ethiopic", ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, sans-serif`;
  if (!f || /^system(-ui)?$/i.test(f)) return fallback;
  return `${/^[\w-]+$/.test(f) ? f : `"${f.replace(/"/g, "")}"`}, ${fallback}`;
}

export function ensureFont(family: string) {
  const f = family.trim();
  if (!f || /^system/i.test(f) || loadedFonts.has(f) || f === "Inter" || typeof document === "undefined") return;
  loadedFonts.add(f);
  const link = document.createElement("link");
  link.rel = "stylesheet";
  link.href = `https://fonts.googleapis.com/css2?family=${encodeURIComponent(f).replace(/%20/g, "+")}:wght@400;500;600;700;800&display=swap`;
  document.head.appendChild(link);
}

/** Pure DOM side-effect: CSS variables, font, base size, favicon, theme-color. Safe to call repeatedly. */
export function applyBrandingToDom(b: Branding, root: HTMLElement = document.documentElement) {
  const scale = generateScale(b.primaryColor);
  for (const s of SHADES) root.style.setProperty(`--brand-${s}`, scale[s]!);
  root.style.setProperty("--brand-accent", b.accentColor);
  root.style.setProperty("--brand-accent-strong", darken(b.accentColor, 0.14));
  root.style.setProperty("--brand-font", fontStack(b.fontFamily));
  root.style.setProperty("--brand-font-size", `${b.baseFontSize}px`);
  ensureFont(b.fontFamily);
  if (root === document.documentElement) {
    let icon = document.querySelector<HTMLLinkElement>("link[rel~='icon']");
    if (!icon) {
      icon = document.createElement("link");
      icon.rel = "icon";
      document.head.appendChild(icon);
    }
    if (icon.getAttribute("href") !== b.faviconUrl) icon.href = b.faviconUrl;
    document.querySelector("meta[name='theme-color']")?.setAttribute("content", scale[800]!);
  }
}

export function setBranding(raw: Partial<Branding> | null, persist = true) {
  current = sanitize(raw);
  applyBrandingToDom(current);
  if (persist) {
    try {
      localStorage.setItem(LS, JSON.stringify(current));
    } catch { /* ignore */ }
  }
  listeners.forEach((l) => l());
}

export const localText = (m: Partial<LocalText> | undefined, lang: Lang) => (m?.[lang] || "").trim();

let started = false;
/** Fetch public branding once at startup (cached copy already applied synchronously → no flash). 404 → defaults. */
export async function initBranding() {
  applyBrandingToDom(current);
  if (started) return;
  started = true;
  try {
    const res = await fetch("/api/public/branding", { headers: { Accept: "application/json", "X-Requested-With": "btn-ops" } });
    if (!res.ok) return;
    const data = (await res.json()) as Partial<Branding>;
    setBranding(data);
    if (isLang(data.defaultLanguage)) setDefaultLang(data.defaultLanguage);
  } catch { /* offline / old backend: keep defaults */ }
}

/** Document title: "Page · Company". */
export function setDocumentTitle(page?: string) {
  document.title = page ? `${page} · ${current.name}` : current.name;
}

/** Re-read the public branding (after the Super Admin saved / uploaded) and apply it. */
export async function refreshBranding(): Promise<Branding> {
  const res = await fetch(`/api/public/branding?_=${Date.now()}`, { headers: { Accept: "application/json", "X-Requested-With": "btn-ops" }, cache: "no-store" });
  if (!res.ok) return current;
  setBranding((await res.json()) as Partial<Branding>);
  return current;
}
