import { useQuery } from "@tanstack/react-query";
import { get } from "@/lib/api";
import { withOfflineCache } from "@/offline/hooks";

/** STK-01 location kind. Production and attendance happen only at SITE; everything else at any kind. */
export type LocationKind = "SITE" | "HQ" | "WAREHOUSE";

/** One ADM-01 reference-data item (name already in the request language). */
export interface RefItem {
  id: string;
  kind: string;
  code: string;
  name: string;
  names: { en: string; am: string; rw: string; fr: string };
  isActive: boolean;
  sortOrder: number;
  metadata: Record<string, unknown>;
}

export interface Lookups {
  plants: { id: string; code: string; name: string; kind?: LocationKind }[];
  products: { id: string; code: string; name: string; unit: string; dimensions: string | null; defaultPrice: number }[];
  materials: { id: string; code: string; name: string; unit: string }[];
  customers: { id: string; name: string }[];
  roles: { value: string; label: string }[];
  /** Active reference data by kind (defect_reason, exception_flag, unit, …). */
  refdata?: Record<string, RefItem[]>;
  locationKinds?: LocationKind[];
}

/** Production sites only (production and attendance pickers). Missing kind (old API) counts as SITE. */
export const sitesOnly = <P extends { kind?: LocationKind }>(plants: P[] | undefined): P[] => (plants ?? []).filter((p) => (p.kind ?? "SITE") === "SITE");

export function useLookups() {
  // offline-first: the last lookups are kept encrypted on the device so forms work without a connection (F4)
  return useQuery({ queryKey: ["lookups"], queryFn: withOfflineCache("lookups", () => get<Lookups>("/lookups")), staleTime: 5 * 60_000, networkMode: "always" });
}
