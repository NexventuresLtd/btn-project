import { useQuery } from "@tanstack/react-query";
import { get } from "@/lib/api";

export interface Lookups {
  plants: { id: string; code: string; name: string }[];
  products: { id: string; code: string; name: string; unit: string; dimensions: string | null; defaultPrice: number }[];
  materials: { id: string; code: string; name: string; unit: string }[];
  customers: { id: string; name: string }[];
  roles: { value: string; label: string }[];
}

export function useLookups() {
  return useQuery({ queryKey: ["lookups"], queryFn: () => get<Lookups>("/lookups"), staleTime: 5 * 60_000 });
}
