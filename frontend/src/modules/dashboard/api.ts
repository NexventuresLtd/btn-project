import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { get } from "@/lib/api";

export type KpiKey =
  | "produced" | "good" | "achievement" | "quality" | "rejection" | "availability" | "cementPer1000" | "costPerBlock"
  | "revenue" | "blocksSold" | "collected" | "receivables" | "pettyCashBalance" | "pettyCashExpenses" | "labourCost";

export interface Kpi {
  key: KpiKey;
  label: string;
  definition: string;
  unit: "percent" | "number" | "rwf";
  better: "up" | "down";
  /** pp = percentage-point difference; relative = fraction (0.12 = +12%) */
  changeKind: "pp" | "relative";
  value: number | null;
  previous: number | null;
  change: number | null;
}

export interface PlantRef {
  id: string;
  code: string;
  name: string;
}

export interface ActionItem {
  key: string;
  label: string;
  description: string;
  count: number;
  to: string;
  permission: string;
}

export interface Dashboard {
  range: { from: string; to: string; days: number; granularity: "day" | "week" | "month" };
  previousRange: { from: string; to: string };
  plant: PlantRef | null;
  plants: PlantRef[];
  plantScoped: boolean;
  user: { name: string; role: string; roleLabel: string };
  generatedAt: string;
  sections: string[];
  kpis: Kpi[];
  production: null | {
    totals: { target: number; produced: number; damageProd: number; damageHand: number; samples: number; good: number; reports: number; downtimeMins: number; plannedMins: number; cementBags: number };
    availabilityBasis: string;
    byPlant: { plantId: string; code: string; name: string; reports: number; target: number; produced: number; good: number; damage: number; samples: number; downtimeMins: number; achievement: number | null; quality: number | null; rejection: number | null; availability: number | null; cementPer1000: number | null }[];
    byProduct: { productId: string; code: string; name: string; produced: number; good: number; target: number }[];
    trend: { date: string; total: number; good: number; plants: Record<string, number> }[];
  };
  stock: null | {
    asOf: string;
    total: number;
    items: { productId: string; code: string; name: string; unit: string; quantity: number; byPlant: Record<string, number> }[];
  };
  materials: null | {
    alerts: { plantId: string; plantCode: string; materialId: string; code: string; name: string; unit: string; balance: number; minLevel: number; severity: "out" | "low" }[];
  };
  sales: null | {
    revenue: number;
    blocksSold: number;
    salesCount: number;
    collected: number;
    receivables: number;
    previous: { revenue: number; blocksSold: number; salesCount: number; collected: number; receivables: number };
    trend: { date: string; revenue: number; collected: number }[];
    topCustomers: { customerId: string; name: string; revenue: number; blocks: number; sales: number }[];
    topProducts: { productId: string; code: string; name: string; revenue: number; blocks: number }[];
  };
  pettycash: null | {
    balance: number;
    openingBalance: number;
    deposits: number;
    expenses: number;
    byCategory: { category: string; amount: number; charges: number; count: number }[];
    balanceBasis: string;
  };
  payroll: null | { labourCost: number; gross: number; periods: number; workers: number };
  cost: null | {
    value: number | null;
    labour: number;
    pettyCash: number;
    materials: number;
    goodBlocks: number;
    total: number;
    unpricedMaterials: string[];
    basis: string;
  };
  actions: ActionItem[];
  activity: null | { id: string; at: string; action: string; entity: string; entityId: string | null; summary: string | null; user: { id: string; name: string } | null }[];
}

export function useDashboard(query: string) {
  return useQuery({
    queryKey: ["dashboard", query],
    queryFn: ({ signal }) => get<Dashboard>(`/dashboard${query}`, signal),
    placeholderData: keepPreviousData,
    staleTime: 60_000,
    refetchInterval: 5 * 60_000,
  });
}
