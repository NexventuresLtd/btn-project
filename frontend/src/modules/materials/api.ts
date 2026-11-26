import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { get, post, qs } from "@/lib/api";

export type MovementType = "OPENING" | "RECEIPT" | "TRANSFER_OUT" | "TRANSFER_IN" | "CONSUMPTION" | "ISSUE" | "ADJUSTMENT";

export interface PlantRef {
  id: string;
  code: string;
  name: string;
}

export interface MaterialInfo {
  id: string;
  code: string;
  name: string;
  unit: string;
  minLevel: number;
}

export interface Options {
  plants: PlantRef[];
  materials: MaterialInfo[];
}

export interface BalanceCell {
  plantId: string;
  materialId: string;
  balance: number;
  minLevel: number;
  tracked: boolean;
  belowMin: boolean;
  shortfall: number;
}

export interface Balances {
  date: string;
  plants: PlantRef[];
  materials: MaterialInfo[];
  cells: BalanceCell[];
}

export interface DailyRow {
  materialId: string;
  code: string;
  name: string;
  unit: string;
  minLevel: number;
  opening: number;
  received: number;
  receivedFromSuppliers: number;
  receivedFrom: Record<string, number>;
  transferredOut: number;
  transferredTo: Record<string, number>;
  used: number;
  adjustment: number;
  closing: number;
  belowMin: boolean;
  active: boolean;
}

export interface PlantDaily {
  plant: PlantRef;
  destinations: string[];
  days: { date: string; active: boolean; rows: DailyRow[] }[];
}

export interface Daily {
  from: string;
  to: string;
  materials: MaterialInfo[];
  plants: PlantDaily[];
}

export interface Movement {
  id: string;
  date: string;
  type: MovementType;
  quantity: number;
  unitCost: number | null;
  value: number | null;
  supplier: string | null;
  reference: string | null;
  note: string | null;
  plant: PlantRef;
  material: { id: string; code: string; name: string; unit: string };
  counterpartPlant: PlantRef | null;
  transferGroup: string | null;
  lotRef?: string | null;
  deviceId?: string | null;
  sourceType: string | null;
  sourceId: string | null;
  source: string;
  sourceNumber: string | null;
  createdBy: { id: string; name: string } | null;
  createdAt: string;
}

export interface MovementList {
  items: Movement[];
  total: number;
  page: number;
  pageSize: number;
}

export interface OpeningRecord {
  plantId: string;
  materialId: string;
  date: string;
  quantity: number;
}

export function useMaterialOptions() {
  return useQuery({ queryKey: ["materials", "options"], queryFn: () => get<Options>("/materials/options"), staleTime: 5 * 60_000 });
}

export function useBalances(params: { date?: string; plantId?: string }, enabled = true) {
  return useQuery({
    queryKey: ["materials", "balances", params],
    queryFn: ({ signal }) => get<Balances>(`/materials/balances${qs(params)}`, signal),
    placeholderData: keepPreviousData,
    enabled,
  });
}

export function useDaily(params: { from: string; to: string; plantId?: string }) {
  return useQuery({
    queryKey: ["materials", "daily", params],
    queryFn: ({ signal }) => get<Daily>(`/materials/daily${qs(params)}`, signal),
    placeholderData: keepPreviousData,
  });
}

export function useMovements(params: Record<string, string | number | undefined>) {
  return useQuery({
    queryKey: ["materials", "movements", params],
    queryFn: ({ signal }) => get<MovementList>(`/materials/movements${qs(params)}`, signal),
    placeholderData: keepPreviousData,
  });
}

export function useOpenings(plantId: string | undefined, enabled: boolean) {
  return useQuery({
    queryKey: ["materials", "openings", plantId],
    queryFn: ({ signal }) => get<{ items: OpeningRecord[] }>(`/materials/openings${qs({ plantId })}`, signal),
    enabled: enabled && !!plantId,
  });
}

export interface ReceiptInput {
  plantId: string;
  date: string;
  materialId: string;
  quantity: string;
  unitCost: string;
  supplier: string;
  reference: string;
  lotRef: string;
  note: string;
}
export interface ConsumptionInput {
  plantId: string;
  date: string;
  materialId: string;
  quantity: string;
  lotRef: string;
  note: string;
}
export interface AdjustmentInput {
  plantId: string;
  date: string;
  materialId: string;
  quantity: string;
  reason: string;
}
export interface OpeningInput {
  plantId: string;
  date: string;
  note: string;
  lines: { materialId: string; quantity: string }[];
}

export const recordReceipt = (v: ReceiptInput) => post("/materials/receipts", { ...v });
export const recordConsumption = (v: ConsumptionInput) => post("/materials/consumption", { ...v });
export const recordAdjustment = (v: AdjustmentInput) => post("/materials/adjustments", { ...v });
export const recordOpening = (v: OpeningInput) => post("/materials/opening", { ...v, lines: v.lines.map((l) => ({ ...l })) });
