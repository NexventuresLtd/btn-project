import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { get, qs } from "@/lib/api";
import { useReportCache } from "@/offline/cache";

/** Order lifecycle (SRS Figure 3). */
export type SaleStatus = "DRAFT" | "QUOTED" | "CONFIRMED" | "IN_PRODUCTION" | "FROM_STOCK" | "READY" | "DISPATCHED" | "DELIVERED" | "CLOSED" | "CANCELLED";
export const ORDER_STATUSES: SaleStatus[] = ["DRAFT", "QUOTED", "CONFIRMED", "IN_PRODUCTION", "FROM_STOCK", "READY", "DISPATCHED", "DELIVERED", "CLOSED", "CANCELLED"];
/** The main path of Figure 3 (IN_PRODUCTION / FROM_STOCK share one step). */
export const LIFECYCLE_PATH: SaleStatus[][] = [["DRAFT"], ["QUOTED"], ["CONFIRMED"], ["IN_PRODUCTION", "FROM_STOCK"], ["READY"], ["DISPATCHED"], ["DELIVERED"], ["CLOSED"]];
export const isEditable = (s: SaleStatus) => s === "DRAFT" || s === "QUOTED";
export const isCommitted = (s: SaleStatus) => !["DRAFT", "QUOTED", "CANCELLED"].includes(s);
export const isPayable = (s: SaleStatus) => ["CONFIRMED", "IN_PRODUCTION", "FROM_STOCK", "READY", "DISPATCHED", "DELIVERED"].includes(s);
export const isDispatchable = (s: SaleStatus) => s === "READY" || s === "DISPATCHED";
export const isAmendable = (s: SaleStatus) => ["CONFIRMED", "IN_PRODUCTION", "FROM_STOCK", "READY", "DISPATCHED", "DELIVERED"].includes(s);
export type TransportMode = "CLIENT" | "BTN";
export type PaymentKind = "DEPOSIT" | "PART" | "BALANCE";
export type CustomerStatus = "ACTIVE" | "INACTIVE" | "BLOCKED";
export type CommKind = "CALL" | "EMAIL" | "MEETING" | "NOTE";
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
  transportMode: TransportMode;
  dueDate: string | null;
  overdue: boolean;
  pendingApproval: ApprovalKind | null;
  version: number;
}

export interface SaleList {
  items: SaleRow[];
  total: number;
  page: number;
  pageSize: number;
  pageTotals: Money;
  rangeTotals: Money;
  statusCounts: Record<"ALL" | SaleStatus, number> & { OPEN_DRAFT: number; COMMITTED: number };
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
  gross: number;
  discountPercent: number | null;
  discountAmount: number;
  fulfilment: "STOCK" | "PRODUCTION" | null;
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
  destination: string | null;
  transportMode: TransportMode;
  deliveryNote: string | null;
  hasPhoto: boolean;
  capturedOffline: boolean;
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
  clientType?: string | null;
  status?: CustomerStatus;
  notes?: string | null;
  paymentTerm?: string | null;
}

export interface Contact {
  id?: string;
  name: string;
  role: string | null;
  phone: string | null;
  email: string | null;
  isPrimary: boolean;
}

export interface CustomerProfile {
  customer: Customer & { status: CustomerStatus };
  clientTypeName: string | null;
  paymentTermName: string | null;
  blockedReason: string | null;
  contacts: Contact[];
  summary: { orders: number; openOrders: number; totalSales: number; paid: number; outstanding: number; overdue: number; lastOrder: string | null; communications: number };
}

export interface DuplicateCandidate {
  id: string;
  name: string;
  tin: string | null;
  phone: string | null;
  status: CustomerStatus;
  score: number;
  match: ("tin" | "phone" | "name")[];
}

export interface Communication {
  id: string;
  kind: CommKind;
  subject: string;
  body: string | null;
  contactName: string | null;
  occurredAt: string;
  saleId: string | null;
  saleNumber: string | null;
  by: Person;
  createdAt: string;
}

export type ApprovalKind = "CONFIRM" | "CANCEL" | "AMENDMENT";
export interface ApprovalLevel {
  level: number;
  permission: string;
  role: string | null;
  label: string;
}
export interface Approval {
  id: string;
  saleId: string;
  saleNumber?: string;
  customer?: string;
  kind: ApprovalKind;
  status: "PENDING" | "APPROVED" | "REJECTED" | "WITHDRAWN";
  amount: number;
  reason: string | null;
  amendmentId: string | null;
  levels: ApprovalLevel[];
  steps: { level: number; label: string; decision: "APPROVED" | "REJECTED"; by: Person; at: string; note: string | null; auto: boolean }[];
  nextLevel: ApprovalLevel | null;
  canDecide: boolean;
  requestedBy: Person;
  requestedAt: string;
  decidedAt: string | null;
}

export interface AmendmentLine {
  saleLineId?: string;
  productId: string;
  quantity: number;
  unitPrice: number;
  discountPercent: number | null;
  discountAmount: number;
}
export interface Amendment {
  id: string;
  status: "PENDING" | "APPLIED" | "REJECTED" | "WITHDRAWN";
  fromVersion: number;
  toVersion: number | null;
  reason: string;
  proposed: { lines: AmendmentLine[]; paymentTerm: string | null; transportMode: TransportMode; deliveryAddress: string | null; deliverySite: string | null; requestedDate: string | null; deliveryZone: string | null; distanceKm: number | null };
  before: { total: number; lines: { code: string; quantity: number; unitPrice: number; amount: number; kind: string }[] };
  oldTotal: number;
  newTotal: number;
  requestedBy: Person;
  requestedAt: string;
  decidedAt: string | null;
}
export interface OrderVersion {
  version: number;
  total: number;
  snapshot: { lines: { code: string; kind: string; quantity: number; unitPrice: number; discountAmount: number; amount: number }[]; paymentTerm: string | null; transportMode: TransportMode; deliveryCharge: number };
  amendmentId: string | null;
  createdBy: Person;
  createdAt: string;
}

export interface LineAvailability {
  saleLineId: string;
  productId: string;
  productCode: string;
  productName: string;
  ordered: number;
  delivered: number;
  remaining: number;
  available: number;
  reserved: number;
  free: number;
  shortfall: number;
  fulfilment: string;
}

export interface ChargeRule {
  id: string;
  name: string;
  method: "FIXED" | "ZONE" | "DISTANCE";
  zone: string | null;
  minKm: number | null;
  maxKm: number | null;
  amount: number;
  isActive: boolean;
  sortOrder: number;
}

export interface RefItem {
  id: string;
  code: string;
  name: string;
  metadata?: { days?: number; depositPercent?: number } | null;
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
  payments: { id: string; date: string; amount: number; method: PaymentMethod; methodLabel: string; reference: string | null; receivedBy: Person | null; createdAt: string; kind: PaymentKind; /** the income record this payment is (finance records) */ incomeId?: string | null; incomeNumber?: string | null }[];
  returns: { id: string; date: string; plant: Ref; productId: string; product: ProductRef; quantity: number; note: string | null; createdBy: Person | null; createdAt: string }[];
  returnable: { productId: string; product: ProductRef | null; delivered: number; returned: number; returnable: number }[];
  totals: { quantity: number; amount: number; paid: number; balance: number; delivered: number; remaining: number; returned: number; gross: number; discount: number; charge: number; subtotal: number };
  paymentStatus: PaymentStatus;
  deliveryStatus: DeliveryProgress;
  needsGmApproval: boolean;
  maxDiscountPercent: number;
  legacyStatus: "DRAFT" | "CONFIRMED" | "CANCELLED";
  nextStatuses: SaleStatus[];
  paymentTerm: string | null;
  paymentTermName: string | null;
  depositAmount: number;
  depositStatus: "NONE" | "DUE" | "PARTIAL" | "RECEIVED";
  dueDate: string | null;
  overdue: boolean;
  transportMode: TransportMode;
  deliveryAddress: string | null;
  deliverySite: string | null;
  requestedDate: string | null;
  deliveryZone: string | null;
  deliveryZoneName: string | null;
  distanceKm: number | null;
  deliveryCharge: number;
  chargeLine: { id: string; description: string | null; amount: number } | null;
  validUntil: string | null;
  quotedBy: Person | null;
  quotedAt: string | null;
  readyAt: string | null;
  closedBy: Person | null;
  closedAt: string | null;
  version: number;
  capturedOffline: boolean;
  locked: boolean;
  pendingApproval: Approval | null;
  approvals: Approval[];
  amendments: Amendment[];
  versions: OrderVersion[];
  history: { from: SaleStatus | null; to: SaleStatus; by: Person | null; at: string; note: string | null }[];
  communications: Communication[];
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
  statuses: SaleStatus[];
  paymentTerms: RefItem[];
  deliveryZones: RefItem[];
  clientTypes: RefItem[];
  transportModes: TransportMode[];
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
  destination: string | null;
  transportMode: TransportMode;
  deliveryNote: string | null;
  hasPhoto: boolean;
  capturedOffline: boolean;
}

export interface PendingDelivery {
  saleId: string;
  number: string;
  status: SaleStatus;
  date: string;
  requestedDate: string | null;
  late: boolean;
  customer: { id: string; name: string };
  plant: Ref;
  transportMode: TransportMode;
  destination: string | null;
  ordered: number;
  shipped: number;
  remaining: number;
  openNotes: number;
}

export interface OrdersReport {
  groupBy: "client" | "product" | "status" | "period";
  period: "day" | "week" | "month";
  range: { from: string; to: string };
  rows: { key: string; label: string; code?: string; orders: number; quantity: number; amount: number; paid: number; outstanding: number; delivered: number }[];
  totals: { orders: number; quantity: number; amount: number; paid: number; outstanding: number; delivered: number };
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
  contacts: number;
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
  profile: (id: string) => ["sales", "profile", id] as const,
  comms: (id: string, q: object) => ["sales", "comms", id, q] as const,
  customerOrders: (id: string, q: object) => ["sales", "customerOrders", id, q] as const,
  approvals: (q: object) => ["sales", "approvals", q] as const,
  charges: ["sales", "charges"] as const,
  pending: (q: object) => ["sales", "pending", q] as const,
  report: (q: object) => ["sales", "report", q] as const,
};

export function useCustomerProfile(id: string | undefined) {
  return useQuery({ queryKey: salesKeys.profile(id ?? ""), queryFn: ({ signal }) => get<CustomerProfile>(`/sales/customers/${id}/profile`, signal), enabled: !!id });
}

export function useCommunications(id: string | undefined, q: { q?: string; kind?: string }) {
  return useQuery({ queryKey: salesKeys.comms(id ?? "", q), queryFn: ({ signal }) => get<{ items: Communication[] }>(`/sales/customers/${id}/communications${qs(q)}`, signal), enabled: !!id, placeholderData: keepPreviousData });
}

export function useCustomerOrders(id: string | undefined, q: { q?: string; status?: string; page?: number }) {
  return useQuery({ queryKey: salesKeys.customerOrders(id ?? "", q), queryFn: ({ signal }) => get<SaleList>(`/sales/customers/${id}/orders${qs(q)}`, signal), enabled: !!id, placeholderData: keepPreviousData });
}

export function useApprovals(q: { status?: string; mine?: string }) {
  return useQuery({ queryKey: salesKeys.approvals(q), queryFn: ({ signal }) => get<{ items: Approval[] }>(`/sales/approvals${qs(q)}`, signal), placeholderData: keepPreviousData });
}

export function useChargeRules(enabled = true) {
  return useQuery({ queryKey: salesKeys.charges, queryFn: ({ signal }) => get<{ items: ChargeRule[]; zones: RefItem[] }>("/sales/delivery-charges", signal), enabled, staleTime: 60_000 });
}

export function usePendingDeliveries(q: { plantId?: string; customerId?: string; transportMode?: string; q?: string }) {
  return useQuery({ queryKey: salesKeys.pending(q), queryFn: ({ signal }) => get<{ items: PendingDelivery[]; total: number; remaining: number }>(`/sales/deliveries/pending${qs(q)}`, signal), placeholderData: keepPreviousData });
}

export function useOrdersReport(q: Record<string, string | undefined>) {
  const cached = useReportCache();
  return useQuery({ queryKey: salesKeys.report(q), networkMode: "always", queryFn: cached("sales:orders-report", qs(q), (signal) => get<OrdersReport>(`/sales/reports/orders${qs(q)}`, signal)), placeholderData: keepPreviousData });
}

export function useLineAvailability(saleId: string, enabled: boolean) {
  return useQuery({
    queryKey: ["sales", "availability", saleId, "lines"],
    queryFn: ({ signal }) => get<{ lines: LineAvailability[]; shortfall: number }>(`/sales/${saleId}/availability`, signal),
    enabled,
  });
}

/** Line money: percent discount wins over an amount discount; mirrors backend pricing.go lineAmounts. */
export function lineMoney(qty: number, price: number, pct: number | null, amount: number) {
  const gross = qty * price;
  let disc = 0;
  if (pct != null && pct > 0) disc = Math.floor((gross * Math.min(100, pct)) / 100 + 0.5);
  else if (amount > 0) disc = Math.min(amount, gross);
  return { gross, discount: disc, net: gross - disc };
}

/** Effective-price GM rule on the net line amount (backend netNeedsGmApproval). */
export function netNeedsGmApproval(net: number, qty: number, defaultPrice: number, maxDiscountPercent: number) {
  if (!(defaultPrice > 0) || qty <= 0) return false;
  return net * 100 < defaultPrice * qty * (100 - maxDiscountPercent);
}

/** Delivery charge precedence (backend pickCharge): zone → distance band → fixed. */
export function pickCharge(rules: ChargeRule[], zone: string, km: number | null): { amount: number; rule: ChargeRule | null } {
  const act = rules.filter((r) => r.isActive).sort((a, b) => a.sortOrder - b.sortOrder);
  if (zone) {
    const z = act.find((r) => r.method === "ZONE" && r.zone === zone);
    if (z) return { amount: z.amount, rule: z };
  }
  if (km != null) {
    const d = act.find((r) => r.method === "DISTANCE" && r.minKm != null && km >= r.minKm && (r.maxKm == null || km < r.maxKm));
    if (d) return { amount: d.amount, rule: d };
  }
  const f = act.find((r) => r.method === "FIXED");
  return f ? { amount: f.amount, rule: f } : { amount: 0, rule: null };
}

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
  transportMode?: string;
  approval?: string;
  overdue?: string;
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

export function useDeliveries(q: { from: string; to: string; plantId?: string; status?: string; q?: string; page?: number; customerId?: string; saleId?: string; transportMode?: string }) {
  return useQuery({ queryKey: salesKeys.deliveries(q), queryFn: ({ signal }) => get<DeliveryList>(`/sales/deliveries${qs({ ...q })}`, signal), placeholderData: keepPreviousData });
}

export function useCustomers(q: { q?: string; sort?: string; page?: number; pageSize?: number; status?: string; clientType?: string }) {
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
