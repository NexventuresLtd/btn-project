import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { get, qs } from "@/lib/api";

export type SaleStatus = "DRAFT" | "CONFIRMED" | "CANCELLED";
export type PaymentStatus = "UNPAID" | "PARTIAL" | "PAID";
export type DeliveryProgress = "NOT_DELIVERED" | "PARTIAL" | "DELIVERED";
export type DeliveryStatus = "DISPATCHED" | "DELIVERED" | "CANCELLED";
export type PaymentMethod = "CASH" | "MOMO" | "BANK_BK" | "BANK_OTHER" | "CHEQUE";

export const PAYMENT_METHODS: { value: PaymentMethod }[] = [
  { value: "MOMO" },
  { value: "BANK_BK" },
  { value: "BANK_OTHER" },
  { value: "CASH" },
  { value: "CHEQUE" },
];

export interface Ref {
  id: string;
  code: string;
  name: string;
}
export interface Person {
  id: string;
  name: string;
}
export interface ProductRef {
  id: string;
  code: string;
  name: string;
  unit: string;
  dimensions: string | null;
  defaultPrice: number;
}

export interface Money {
  amount: number;
  quantity: number;
  paid: number;
  outstanding: number;
}

export interface SaleRow {
  id: string;
  number: string;
  date: string;
  status: SaleStatus;
  plant: Ref;
  customer: { id: string; name: string };
  invoiceType: string;
  ebmInvoiceNo: string | null;
  ebmReceiptNo: string | null;
  products: { code: string; name: string; quantity: number; unitPrice: number }[];
  dnNumbers: string[];
  quantity: number;
  amount: number;
  paid: number;
  outstanding: number;
  delivered: number;
  paymentStatus: PaymentStatus;
  deliveryStatus: DeliveryProgress;
  needsGmApproval: boolean;
}

export interface SaleList {
  items: SaleRow[];
  total: number;
  page: number;
  pageSize: number;
  pageTotals: Money;
  rangeTotals: Money;
  statusCounts: Record<"ALL" | SaleStatus, number>;
  kpis: { sales: number; amount: number; quantity: number; paid: number; outstanding: number; drafts: number };
  range: { from: string; to: string };
}

export interface SaleLine {
  id: string;
  productId: string;
  product: ProductRef;
  quantity: number;
  unitPrice: number;
  amount: number;
  defaultPrice: number;
  minPrice: number;
  discount: number | null;
  needsGmApproval: boolean;
  delivered: number;
  remaining: number;
}

export interface DeliveryDetail {
  id: string;
  dnNumber: string;
  date: string;
  plant: Ref;
  vehicle: string | null;
  driver: string | null;
  status: DeliveryStatus;
  deliveredAt: string | null;
  receivedBy: string | null;
  createdBy: Person | null;
  createdAt: string;
  totalQty: number;
  lines: { id: string; saleLineId: string; productId: string; product: ProductRef; quantity: number }[];
}

export interface Customer {
  id: string;
  name: string;
  tin: string | null;
  phone: string | null;
  email: string | null;
  address: string | null;
  isActive: boolean;
  createdAt?: string;
}

export interface SaleDetail {
  id: string;
  number: string;
  status: SaleStatus;
  date: string;
  invoiceType: string;
  ebmInvoiceNo: string | null;
  ebmReceiptNo: string | null;
  notes: string | null;
  plant: Ref;
  customer: Customer;
  createdBy: Person | null;
  createdAt: string;
  confirmedBy: Person | null;
  confirmedAt: string | null;
  cancelledBy: Person | null;
  cancelledAt: string | null;
  cancelReason: string | null;
  lines: SaleLine[];
  deliveries: DeliveryDetail[];
  payments: { id: string; date: string; amount: number; method: PaymentMethod; methodLabel: string; reference: string | null; receivedBy: Person | null; createdAt: string }[];
  returns: { id: string; date: string; plant: Ref; productId: string; product: ProductRef; quantity: number; note: string | null; createdBy: Person | null; createdAt: string }[];
  returnable: { productId: string; product: ProductRef | null; delivered: number; returned: number; returnable: number }[];
  totals: { quantity: number; amount: number; paid: number; balance: number; delivered: number; remaining: number; returned: number };
  paymentStatus: PaymentStatus;
  deliveryStatus: DeliveryProgress;
  needsGmApproval: boolean;
  maxDiscountPercent: number;
}

export interface StockWarning {
  productId: string;
  productCode: string;
  productName: string;
  required: number;
  available: number;
  message: string;
}

export interface SalesMeta {
  maxDiscountPercent: number;
  canApproveDiscount: boolean;
}

export interface DeliveryRow {
  id: string;
  dnNumber: string;
  date: string;
  status: DeliveryStatus;
  plant: Ref;
  sale: { id: string; number: string };
  customer: { id: string; name: string };
  vehicle: string | null;
  driver: string | null;
  receivedBy: string | null;
  deliveredAt: string | null;
  totalQty: number;
  products: { code: string; name: string; quantity: number }[];
}

export interface DeliveryList {
  items: DeliveryRow[];
  total: number;
  page: number;
  pageSize: number;
  rangeTotals: { quantity: number };
  statusCounts: Partial<Record<DeliveryStatus, number>>;
}

export interface CustomerRow extends Customer {
  salesCount: number;
  totalSales: number;
  paid: number;
  outstanding: number;
  lastSaleDate: string | null;
}

export interface CustomerList {
  items: CustomerRow[];
  total: number;
  page: number;
  pageSize: number;
  totals: { totalSales: number; paid: number; outstanding: number; withBalance: number };
}

export interface CustomerStatement {
  customer: Customer;
  range: { from: string; to: string };
  summary: { salesCount: number; totalSales: number; paid: number; outstanding: number; lastSaleDate: string | null };
  statement: {
    opening: number;
    debit: number;
    credit: number;
    closing: number;
    rows: { date: string; kind: "SALE" | "PAYMENT"; reference: string; description: string; debit: number; credit: number; balance: number; saleId?: string }[];
  };
  openSales: { id: string; number: string; date: string; amount: number; paid: number; outstanding: number }[];
}

export type AgeKey = "d0_30" | "d31_60" | "d61_90" | "d90p";
export const AGE_BUCKETS: { key: AgeKey }[] = [{ key: "d0_30" }, { key: "d31_60" }, { key: "d61_90" }, { key: "d90p" }];

export interface Receivables {
  asOf: string;
  rows: ({ customerId: string; customerName: string; openSales: number; oldestDate: string | null; total: number } & Record<AgeKey, number>)[];
  totals: Record<AgeKey, number> & { total: number; openSales: number };
  sales: { id: string; number: string; date: string; customerId: string; customerName: string; plantCode: string; amount: number; paid: number; outstanding: number; days: number }[];
}

// ───────────────────────────── queries ─────────────────────────────

export const salesKeys = {
  all: ["sales"] as const,
  list: (q: object) => ["sales", "list", q] as const,
  detail: (id: string) => ["sales", "detail", id] as const,
  deliveries: (q: object) => ["sales", "deliveries", q] as const,
  customers: (q: object) => ["sales", "customers", q] as const,
  customer: (id: string, q: object) => ["sales", "customer", id, q] as const,
  receivables: (q: object) => ["sales", "receivables", q] as const,
  meta: ["sales", "meta"] as const,
};

export function useSalesMeta() {
  return useQuery({ queryKey: salesKeys.meta, queryFn: () => get<SalesMeta>("/sales/meta"), staleTime: 5 * 60_000 });
}

export interface SalesQuery {
  from: string;
  to: string;
  plantId?: string;
  customerId?: string;
  status?: string;
  paymentStatus?: string;
  deliveryStatus?: string;
  q?: string;
  page?: number;
}

export function useSales(q: SalesQuery) {
  return useQuery({ queryKey: salesKeys.list(q), queryFn: ({ signal }) => get<SaleList>(`/sales${qs({ ...q })}`, signal), placeholderData: keepPreviousData });
}

export function useSale(id: string | undefined) {
  return useQuery({ queryKey: salesKeys.detail(id ?? ""), queryFn: ({ signal }) => get<{ sale: SaleDetail }>(`/sales/${id}`, signal).then((r) => r.sale), enabled: !!id });
}

export function useAvailability(saleId: string, plantId: string, date: string, enabled: boolean) {
  return useQuery({
    queryKey: ["sales", "availability", saleId, plantId, date],
    queryFn: ({ signal }) => get<{ items: { productId: string; available: number }[] }>(`/sales/${saleId}/availability${qs({ plantId, date })}`, signal),
    enabled: enabled && !!plantId && /^\d{4}-\d{2}-\d{2}$/.test(date),
  });
}

export function useDeliveries(q: { from: string; to: string; plantId?: string; status?: string; q?: string; page?: number }) {
  return useQuery({ queryKey: salesKeys.deliveries(q), queryFn: ({ signal }) => get<DeliveryList>(`/sales/deliveries${qs({ ...q })}`, signal), placeholderData: keepPreviousData });
}

export function useCustomers(q: { q?: string; sort?: string; page?: number; pageSize?: number }) {
  return useQuery({ queryKey: salesKeys.customers(q), queryFn: ({ signal }) => get<CustomerList>(`/sales/customers${qs({ ...q })}`, signal), placeholderData: keepPreviousData });
}

export function useCustomerStatement(id: string | undefined, q: { from?: string; to?: string }) {
  return useQuery({ queryKey: salesKeys.customer(id ?? "", q), queryFn: ({ signal }) => get<CustomerStatement>(`/sales/customers/${id}${qs(q)}`, signal), enabled: !!id, placeholderData: keepPreviousData });
}

export function useReceivables(q: { asOf?: string; plantId?: string }) {
  return useQuery({ queryKey: salesKeys.receivables(q), queryFn: ({ signal }) => get<Receivables>(`/sales/receivables${qs(q)}`, signal), placeholderData: keepPreviousData });
}

/** Mirrors the backend rule (modules/sales/calc.ts) for the live warning in the form. */
export function lineNeedsGmApproval(unitPrice: number, defaultPrice: number, maxDiscountPercent: number) {
  if (!(defaultPrice > 0)) return false;
  return unitPrice * 100 < defaultPrice * (100 - maxDiscountPercent);
}

export function minPriceWithoutApproval(defaultPrice: number, maxDiscountPercent: number) {
  return defaultPrice > 0 ? Math.ceil((defaultPrice * (100 - maxDiscountPercent)) / 100) : 0;
}
