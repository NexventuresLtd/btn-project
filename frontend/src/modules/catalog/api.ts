import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { get, qs } from "@/lib/api";
import { ADMIN_KEY } from "@/modules/admin/api";
import { REFDATA_KEY } from "@/modules/admin/refdataApi";

export const INSIGHTS_KEY = ["insights"] as const;

/** Money is `null` when the viewer may not see money (moneyHidden) or the cost is not known. */
export type Money = number | null;

export interface ProductTotals {
  produced: number;
  damaged: number;
  sold: number;
  revenue: Money;
  materialCost: Money;
  grossProfit: Money;
}

export interface ProductInsightItem {
  productId: string;
  code: string;
  name: string;
  /** product_category reference-data code */
  category: string | null;
  unit: string;
  defaultPrice: Money;
  isActive: boolean;
  produced: number;
  damaged: number;
  /** damaged ÷ produced × 100 (produced includes damaged) */
  damagePct: number;
  sold: number;
  revenue: Money;
  materialCost: Money;
  grossProfit: Money;
  /** false: no recipe or no material prices, so materialCost / grossProfit are unknown */
  costKnown: boolean;
  prev: { produced: number; sold: number; revenue: Money; grossProfit: Money };
}

export interface SiteProfit {
  plantId: string;
  code: string;
  name: string;
  revenue: Money;
  materialCost: Money;
  wages: Money;
  expenses: Money;
  profit: Money;
  prevProfit: Money;
  costComplete: boolean;
}

/** GET /api/insights/products?month=YYYY-MM */
export interface ProductsInsights {
  month: string;
  previousMonth: string;
  moneyHidden: boolean;
  totals: ProductTotals;
  previous: ProductTotals;
  costComplete: boolean;
  items: ProductInsightItem[];
  /** [] when moneyHidden; site revenue includes delivery charges, so it need not add up to product revenue */
  sites: SiteProfit[];
}

export interface StoryMonth {
  month: string;
  produced: number;
  damaged: number;
  target: number;
  sold: number;
  revenue: Money;
  materialCost: Money;
  grossProfit: Money;
  orders: number;
}

export interface StorySite {
  plantId: string;
  code: string;
  name: string;
  produced: number;
  damaged: number;
  sold: number;
  revenue: Money;
  productionDays: number;
  avgWorkersPresent: number;
  inCharge: { userId: string; name: string; reports: number }[];
}

/** GET /api/insights/products/:id?month=YYYY-MM&months=6 */
export interface ProductStory {
  month: string;
  moneyHidden: boolean;
  product: { id: string; code: string; name: string; category: string | null; unit: string; defaultPrice: Money; isActive: boolean; dimensions: string | null };
  /** null when money is hidden or a recipe material has no price */
  unitMaterialCost: Money;
  /** a recipe is in force at month end */
  recipeKnown: boolean;
  /** oldest first; the last one is `month` */
  months: StoryMonth[];
  bySite: StorySite[];
  orders: { total: number; byStatus: Record<string, number> };
  defects: { reasonCode: string; quantity: number }[];
}

export function useProductsInsights(month: string) {
  return useQuery({
    queryKey: [...INSIGHTS_KEY, "products", month],
    queryFn: ({ signal }) => get<ProductsInsights>(`/insights/products${qs({ month })}`, signal),
    placeholderData: keepPreviousData,
    retry: 1,
  });
}

export function useProductStory(id: string, month: string, months = 6) {
  return useQuery({
    queryKey: [...INSIGHTS_KEY, "products", id, month, months],
    queryFn: ({ signal }) => get<ProductStory>(`/insights/products/${id}${qs({ month, months })}`, signal),
    placeholderData: keepPreviousData,
    retry: 1,
  });
}

/** Everything a product / material change can affect: the lists, look-ups (dropdowns), insights. */
export const catalogInvalidate = (entity: "products" | "materials") => [[...ADMIN_KEY, "masterdata", entity], ["lookups"], [...INSIGHTS_KEY]];
/** After a category / unit change: the reference-data lists and the look-ups that feed every category select. */
export const REF_INVALIDATE = [[...REFDATA_KEY], ["lookups"]];
