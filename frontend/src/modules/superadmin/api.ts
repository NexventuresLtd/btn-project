import { api, get, put, upload } from "@/lib/api";
import type { Branding } from "@/branding";
import type { Lang } from "@/i18n";

export type BrandingInput = Omit<Branding, "version" | "logoLightUrl" | "logoDarkUrl" | "faviconUrl">;
export type AssetKind = "light" | "dark" | "favicon";

export const saveBranding = (b: BrandingInput) => put<Partial<Branding> | undefined>("/admin/branding", b as unknown as Record<string, unknown>);
export const uploadBrandingAsset = (kind: AssetKind, file: File) => {
  const f = new FormData();
  f.set("kind", kind);
  f.set("file", file);
  return upload<{ url?: string } & Partial<Branding>>("/admin/branding/assets", f);
};
export const removeBrandingAsset = (kind: AssetKind) => api(`/admin/branding/assets/${kind}`, { method: "DELETE" });

export interface Override {
  lang: Lang;
  key: string;
  value: string;
  updatedAt?: string;
}
/** GET /admin/i18n?lang= → overrides for one language; we merge all three. */
export async function listOverrides(signal?: AbortSignal): Promise<Override[]> {
  const out: Override[] = [];
  for (const lang of ["en", "am", "rw"] as Lang[]) {
    const r = await get<unknown>(`/admin/i18n?lang=${lang}`, signal);
    const arr = Array.isArray(r) ? r : ((r as { items?: Override[]; overrides?: Override[] }).items ?? (r as { overrides?: Override[] }).overrides ?? []);
    for (const o of arr as Override[]) out.push({ ...o, lang: o.lang ?? lang });
  }
  return out;
}
export const saveOverride = (o: { lang: Lang; key: string; value: string }) => put("/admin/i18n", o);
export const revertOverride = (lang: Lang, key: string) => api(`/admin/i18n/${lang}/${encodeURIComponent(key)}`, { method: "DELETE" });
