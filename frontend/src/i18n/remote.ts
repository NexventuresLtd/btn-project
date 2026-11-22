import { useEffect } from "react";
import { setOverrides, useLang, type Lang } from "./index";

const inflight = new Map<Lang, Promise<void>>();

/** Super-admin text overrides (win over the static catalogs). 404 / offline → keep shipped text. */
export function loadOverrides(lang: Lang, force = false): Promise<void> {
  if (!force && inflight.has(lang)) return inflight.get(lang)!;
  const p = fetch(`/api/public/i18n/${lang}`, { headers: { Accept: "application/json", "X-Requested-With": "btn-ops" } })
    .then(async (res) => {
      if (!res.ok) return;
      const data = (await res.json()) as { messages?: Record<string, string> };
      setOverrides(lang, data.messages ?? {});
    })
    .catch(() => {});
  inflight.set(lang, p);
  return p;
}

/** Mount once: loads overrides for the active language whenever it changes. */
export function useRemoteOverrides() {
  const lang = useLang();
  useEffect(() => {
    void loadOverrides(lang);
  }, [lang]);
}
