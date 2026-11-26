import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { get, qs } from "@/lib/api";
import type { Tone } from "@/components/ui";

export interface Position {
  date: string;
  plants: { id: string; code: string; name: string }[];
  products: { id: string; code: string; name: string; unit: string; isActive: boolean; byPlant: Record<string, number>; total: number;
    reorderLevel?: number; levels?: Record<string, number>; low?: Record<string, boolean> }[];
  plantTotals: Record<string, number>;
  total: number;
}

export interface DailyFigures {
  opening: number;
  production: number;
  damage1: number;
  sold: number;
  returned: number;
  sample: number;
  damage2: number;
  adjustment: number;
  received: number;
  issued: number;
  transferIn: number;
  transferOut: number;
  closing: number;
}

export interface DailyReport {
  plant: { id: string; code: string; name: string } | null;
  from: string;
  to: string;
  products: { id: string; code: string; name: string; unit: string }[];
  days: { date: string; totals: DailyFigures; rows: (DailyFigures & { productId: string; code: string; name: string; unit: string })[] }[];
}

export type MovementType = "OPENING" | "PRODUCTION" | "DAMAGE_PRODUCTION" | "DAMAGE_HANDLING" | "SAMPLE" | "SALE_ISSUE" | "RETURN" | "ADJUSTMENT"
  | "RECEIPT" | "ISSUE" | "TRANSFER_OUT" | "TRANSFER_IN";

export interface Movement {
  id: string;
  date: string;
  plant: string;
  plantId: string;
  product: { code: string; name: string; unit: string };
  productId: string;
  type: MovementType;
  typeLabel: string;
  quantity: number;
  sourceType: string | null;
  sourceId: string | null;
  sourceLabel: string | null;
  note: string | null;
  createdBy: string | null;
  createdById?: string | null;
  lotRef?: string | null;
  reference?: string | null;
  deviceId?: string | null;
  createdAt: string;
}

export interface MovementList {
  items: Movement[];
  total: number;
  page: number;
  pageSize: number;
  netQuantity: number;
}

export const MOVEMENT_TYPES: { value: MovementType; label: string; tone: Tone }[] = [
  { value: "OPENING", label: "Opening balance", tone: "navy" },
  { value: "PRODUCTION", label: "Production", tone: "green" },
  { value: "DAMAGE_PRODUCTION", label: "Damage at production", tone: "red" },
  { value: "DAMAGE_HANDLING", label: "Damage in handling", tone: "red" },
  { value: "SAMPLE", label: "Sample", tone: "amber" },
  { value: "SALE_ISSUE", label: "Issue / sold", tone: "blue" },
  { value: "RETURN", label: "Returned", tone: "purple" },
  { value: "ADJUSTMENT", label: "Adjustment", tone: "slate" },
  { value: "RECEIPT", label: "Receipt", tone: "green" },
  { value: "ISSUE", label: "Issue", tone: "amber" },
  { value: "TRANSFER_OUT", label: "Transfer out", tone: "purple" },
  { value: "TRANSFER_IN", label: "Transfer in", tone: "blue" },
];

export function usePosition(params: { date?: string; plantId?: string }, enabled = true) {
  return useQuery({
    queryKey: ["stock", "balances", params],
    queryFn: ({ signal }) => get<Position>(`/stock/balances${qs(params)}`, signal),
    placeholderData: keepPreviousData,
    enabled,
  });
}

export function useDaily(params: { from: string; to: string; plantId: string }) {
  return useQuery({
    queryKey: ["stock", "daily", params],
    queryFn: ({ signal }) => get<DailyReport>(`/stock/daily${qs(params)}`, signal),
    placeholderData: keepPreviousData,
  });
}

export function useMovements(params: { from: string; to: string; plantId: string; productId: string; type: string; page: number; userId?: string; lotRef?: string }) {
  return useQuery({
    queryKey: ["stock", "movements", params],
    queryFn: ({ signal }) => get<MovementList>(`/stock/movements${qs(params)}`, signal),
    placeholderData: keepPreviousData,
  });
}
