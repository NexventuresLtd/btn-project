/** Procurement API (backend: internal/procurement, docs/SRS_BUILD_PLAN.md "P6 APIs"). */
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { get, patch, post, qs, upload } from "@/lib/api";
import { withOfflineCache } from "@/offline/hooks";
import { useReportCache } from "@/offline/cache";

export const PROC_KEY = ["procurement"] as const;

export const REQUEST_STATUSES = ["DRAFT", "SUBMITTED", "APPROVED", "REJECTED", "ORDERED", "PARTIALLY_RECEIVED", "RECEIVED", "CLOSED"] as const;
export const ORDER_STATUSES = ["DRAFT", "ORDERED", "PARTIALLY_RECEIVED", "RECEIVED", "CLOSED", "CANCELLED"] as const;
export const SUPPLIER_STATUSES = ["ACTIVE", "ON_HOLD", "BLOCKED", "INACTIVE"] as const;
export const ITEM_TYPES = ["MATERIAL", "PRODUCT", "OTHER"] as const;
export type RequestStatus = (typeof REQUEST_STATUSES)[number];
export type OrderStatus = (typeof ORDER_STATUSES)[number];
export type SupplierStatus = (typeof SUPPLIER_STATUSES)[number];
export type ItemType = (typeof ITEM_TYPES)[number];

export interface Ref { id: string; code: string; name: string }
export interface PlantRef extends Ref { kind: string }
export interface UserRef { id: string; name: string }

export interface TimelineEntry { action: string; fromStatus: string | null; toStatus: string | null; actor: UserRef | null; note: string | null; at: string }
export interface Attachment { id: string; entity: string; entityId: string; kind: string; filename: string; mime: string; size: number; uploadedBy: UserRef; createdAt: string }

export interface RequestRow {
  id: string; number: string; status: RequestStatus; plant: PlantRef; department: string | null; requiredDate: string; estimatedTotal: number;
  source: "MANUAL" | "LOW_STOCK" | "OFFLINE"; requestedBy: UserRef; items: string; lineCount: number; currentLevel: number | null; pendingLabel: string;
  awaitingMe: boolean; createdAt: string; submittedAt: string | null;
}
export interface RequestList { items: RequestRow[]; total: number; page: number; pageSize: number; statusCounts: Record<string, number>; totalValue: number }

export interface RequestLine {
  id: string; lineNo: number; itemType: ItemType; materialId: string | null; productId: string | null; itemCode: string | null; description: string; unit: string;
  quantity: number; estimatedUnitCost: number; estimatedTotal: number; category: string | null; categoryName: string; note: string | null;
  onOrderQty: number; orderedQty: number; receivedQty: number; remainingQty: number;
}
export interface RequiredLevel { level: number; permission: string; role: string | null; label: string; minAmount: number; escalated?: boolean }
export interface ApprovalDecision { cycle: number; level: number; label: string; decision: "APPROVED" | "REJECTED"; by: UserRef; role: string; comment: string | null; at: string }
export interface ApprovalStep extends RequiredLevel { state: "APPROVED" | "REJECTED" | "PENDING" | "WAITING"; decision: ApprovalDecision | null }
export interface OrderBrief { id: string; number: string; status: OrderStatus; supplier: Ref; total: number }

export interface RequestDetail {
  id: string; number: string; status: RequestStatus; plant: PlantRef; department: string | null; departmentName: string; requiredDate: string; justification: string;
  estimatedTotal: number; preferredSupplier: Ref | null; source: string; cycle: number; currentLevel: number | null; requestedBy: UserRef;
  submittedAt: string | null; approvedAt: string | null; rejectedAt: string | null; rejectionReason: string | null; closedAt: string | null; closeReason: string | null;
  createdAt: string; updatedAt: string; lines: RequestLine[]; approvalSteps: ApprovalStep[]; approvalHistory: ApprovalDecision[]; orders: OrderBrief[];
  timeline: TimelineEntry[]; attachments: Attachment[]; approveHint?: string;
  can: { edit: boolean; submit: boolean; recall: boolean; revise: boolean; approve: boolean; reject: boolean; close: boolean; order: boolean };
}

export interface RequestLineInput {
  itemType: ItemType; materialId?: string; productId?: string; description?: string; unit?: string; quantity: number; estimatedUnitCost?: number; category?: string | null; note?: string | null;
}
export interface RequestInput {
  plantId: string; department?: string | null; requiredDate: string; justification: string; preferredSupplierId?: string | null; lines: RequestLineInput[]; submit?: boolean;
}

export interface Contact { name: string; role: string; phone: string; email: string }
export interface SupplierStats { orders: number; openOrders: number; orderedValue: number; receivedValue: number; invoiced: number; lastOrderDate: string | null; exceptions: number }
export interface Supplier {
  id: string; code: string; name: string; tin: string | null; contactName: string | null; phone: string | null; email: string | null; address: string | null;
  contacts: Contact[]; categories: string[]; categoryNames: string[]; paymentTerm: string | null; paymentTermName: string; status: SupplierStatus; notes: string | null;
  createdAt: string; updatedAt: string; stats?: SupplierStats;
}
export interface SupplierDetail extends Supplier { byCategory: SpendRow[] }
export interface SupplierOption { id: string; code: string; name: string; status: SupplierStatus; paymentTerm: string | null }
export interface SupplierInput {
  name: string; tin?: string | null; contactName?: string | null; phone?: string | null; email?: string | null; address?: string | null; contacts?: Contact[];
  categories?: string[]; paymentTerm?: string | null; status?: SupplierStatus; notes?: string | null;
}

export interface OrderRow {
  id: string; number: string; status: OrderStatus; supplier: Ref; deliveryPlant: PlantRef; orderDate: string; expectedDate: string | null; total: number;
  receivedValue: number; invoiced: number; lineCount: number; items: string; requests: string; overdue: boolean; hasException: boolean;
}
export interface OrderList { items: OrderRow[]; total: number; page: number; pageSize: number; statusCounts: Record<string, number> }
export interface OrderLine {
  id: string; lineNo: number; requestLineId: string | null; request: Ref | null; itemType: ItemType; materialId: string | null; productId: string | null; itemCode: string | null;
  description: string; unit: string; category: string | null; categoryName: string; quantity: number; unitPrice: number; total: number; receivedQty: number;
  outstanding: number; closed: boolean; stocked: boolean;
}
export interface ReceiptLine { poLineId: string; description: string; unit: string; quantity: number; lotRef: string | null; variance: "NONE" | "OVER" | "UNDER"; varianceQty: number; reason: string | null; movementId: string | null }
export interface Receipt { id: string; number: string; date: string; plant: PlantRef; deliveryNote: string | null; note: string | null; hasException: boolean; receivedBy: UserRef; createdAt: string; lines: ReceiptLine[]; value: number }
export interface ExpenseRef { id: string; number: string; status: string; amount: number }
export interface Invoice { id: string; invoiceNumber: string; invoiceDate: string; dueDate: string | null; amount: number; receiptId: string | null; receiptNumber: string | null; note: string | null; expense: ExpenseRef | null; createdBy: UserRef; createdAt: string }
export interface OrderDetail {
  id: string; number: string; status: OrderStatus; supplier: Supplier; deliveryPlant: PlantRef; orderDate: string; expectedDate: string | null; paymentTerm: string | null;
  paymentTermName: string; quotationRef: string | null; notes: string | null; total: number; createdBy: UserRef; orderedBy: UserRef | null; orderedAt: string | null;
  closedAt: string | null; closeReason: string | null; cancelledAt: string | null; cancelReason: string | null; createdAt: string; lines: OrderLine[]; receipts: Receipt[];
  invoices: Invoice[]; requests: Ref[]; attachments: Attachment[]; timeline: TimelineEntry[]; receivedValue: number; invoiced: number; overdue: boolean;
  can: { edit: boolean; send: boolean; cancel: boolean; close: boolean; receive: boolean; invoice: boolean; attach: boolean };
}
export interface ApprovedLine {
  requestLineId: string; requestId: string; requestNumber: string; requestStatus: RequestStatus; plant: PlantRef; requiredDate: string; preferredSupplier: Ref | null;
  itemType: ItemType; itemCode: string | null; description: string; unit: string; category: string | null; quantity: number; onOrderQty: number; remainingQty: number; estimatedUnitCost: number;
}
export interface OrderInput {
  supplierId: string; deliveryPlantId: string; orderDate?: string; expectedDate?: string | null; paymentTerm?: string | null; quotationRef?: string | null; notes?: string | null;
  lines: { requestLineId: string; quantity: number; unitPrice: number }[]; send?: boolean;
}
export interface ReceiveInput { plantId?: string; date: string; deliveryNote?: string | null; note?: string | null; lines: { poLineId: string; quantity: number; lotRef?: string | null; final?: boolean; reason?: string | null }[] }
export interface InvoiceInput { invoiceNumber: string; invoiceDate: string; dueDate?: string | null; amount: number; receiptId?: string | null; note?: string | null }

export interface LowItem {
  plant: PlantRef; itemType: "MATERIAL" | "PRODUCT"; itemId: string; code: string; name: string; unit: string; category: string | null; balance: number; level: number;
  shortfall: number; onOrder: number; suggestedQty: number; lastPrice: number; since: string | null;
}

export interface SpendRow { key: string; label: string; orders: number; lines: number; ordered: number; received: number; invoiced: number; outstanding: number }
export interface SpendReport {
  from: string; to: string; groupBy: string; rows: SpendRow[]; byCategory: SpendRow[]; bySupplier: SpendRow[];
  totals: { orders: number; lines: number; ordered: number; received: number; invoiced: number; outstanding: number; suppliers: number };
}
export interface TraceStep { kind: string; id: string; number: string; status: string; statusLabel: string; title: string; date: string; at: string | null; amount: number | null; actor: string; link: string; plantCode: string }
export interface Trace { steps: TraceStep[]; expenses: ExpenseRef[]; financeTraceLink: string }
export interface Summary {
  pendingRequests: { count: number; value: number }; awaitingMyApproval: { count: number; value: number }; approvedNotOrdered: { count: number; value: number };
  openOrders: { count: number; value: number }; awaitingReceipt: { orders: number; lines: number }; overdueOrders: number; exceptions30d: number;
  suppliers: { active: number; total: number }; spendMonth: number; spendYear: number; lowStockItems: number; recent: RequestRow[];
}

type Params = Record<string, string | number | boolean | undefined | null>;

// ── requests ──
export const useRequests = (p: Params) =>
  useQuery({ queryKey: [...PROC_KEY, "requests", p], queryFn: () => get<RequestList>(`/procurement/requests${qs(p)}`), placeholderData: keepPreviousData });
export const useRequest = (id: string | undefined) =>
  useQuery({ queryKey: [...PROC_KEY, "request", id], enabled: !!id, networkMode: "always", queryFn: withOfflineCache(`procurement:request:${id}`, () => get<RequestDetail>(`/procurement/requests/${id}`)) });
export const createRequest = (b: RequestInput) => post<RequestDetail>("/procurement/requests", b as never);
export const updateRequest = (id: string, b: RequestInput) => patch<RequestDetail>(`/procurement/requests/${id}`, b as never);
export const requestAction = (id: string, action: "submit" | "recall" | "revise" | "approve" | "reject" | "close", body: Record<string, unknown> = {}) =>
  post<RequestDetail>(`/procurement/requests/${id}/${action}`, body as never);
export const useLowStock = (plantId: string, enabled = true) =>
  useQuery({ queryKey: [...PROC_KEY, "low-stock", plantId], enabled, queryFn: () => get<{ items: LowItem[]; factor: number; date: string }>(`/procurement/requests/low-stock${qs({ plantId })}`) });
export const createFromLowStock = (b: { plantId: string; requiredDate?: string; items: { itemType: string; itemId: string; quantity: number; estimatedUnitCost?: number }[] }) =>
  post<RequestDetail>("/procurement/requests/from-low-stock", b as never);

// ── suppliers ──
export const useSuppliers = (p: Params) =>
  useQuery({ queryKey: [...PROC_KEY, "suppliers", p], queryFn: () => get<{ items: Supplier[]; total: number; page: number; pageSize: number }>(`/procurement/suppliers${qs(p)}`), placeholderData: keepPreviousData });
export const useSupplier = (id: string | undefined) => useQuery({ queryKey: [...PROC_KEY, "supplier", id], enabled: !!id, queryFn: () => get<SupplierDetail>(`/procurement/suppliers/${id}`) });
export const useSupplierOrders = (id: string, page: number) =>
  useQuery({ queryKey: [...PROC_KEY, "supplier-orders", id, page], queryFn: () => get<OrderList>(`/procurement/suppliers/${id}/orders${qs({ page })}`), placeholderData: keepPreviousData });
export const useSupplierOptions = () =>
  useQuery({ queryKey: [...PROC_KEY, "supplier-options"], staleTime: 60_000, networkMode: "always", queryFn: withOfflineCache("procurement:supplier-options", () => get<{ items: SupplierOption[] }>("/procurement/suppliers/options")) });
export const createSupplier = (b: SupplierInput) => post<Supplier>("/procurement/suppliers", b as never);
export const updateSupplier = (id: string, b: Partial<SupplierInput>) => patch<Supplier>(`/procurement/suppliers/${id}`, b as never);

// ── orders ──
export const useOrders = (p: Params) =>
  useQuery({ queryKey: [...PROC_KEY, "orders", p], queryFn: () => get<OrderList>(`/procurement/orders${qs(p)}`), placeholderData: keepPreviousData });
export const useOrder = (id: string | undefined) => useQuery({ queryKey: [...PROC_KEY, "order", id], enabled: !!id, queryFn: () => get<OrderDetail>(`/procurement/orders/${id}`) });
export const useApprovedLines = (p: Params, enabled = true) =>
  useQuery({ queryKey: [...PROC_KEY, "approved-lines", p], enabled, queryFn: () => get<{ items: ApprovedLine[] }>(`/procurement/orders/approved-lines${qs(p)}`) });
export const createOrder = (b: OrderInput) => post<OrderDetail>("/procurement/orders", b as never);
export const updateOrder = (id: string, b: OrderInput) => patch<OrderDetail>(`/procurement/orders/${id}`, b as never);
export const orderAction = (id: string, action: "send" | "cancel" | "close", body: Record<string, unknown> = {}) => post<OrderDetail>(`/procurement/orders/${id}/${action}`, body as never);
export const receiveOrder = (id: string, b: ReceiveInput) => post<OrderDetail>(`/procurement/orders/${id}/receipts`, b as never);
export const addInvoice = (id: string, b: InvoiceInput) => post<OrderDetail>(`/procurement/orders/${id}/invoices`, b as never);

// ── attachments, trace, reports, summary ──
export function uploadAttachment(entity: string, entityId: string, kind: string, file: File) {
  const f = new FormData();
  f.set("entity", entity);
  f.set("entityId", entityId);
  f.set("kind", kind);
  f.set("file", file);
  return upload<Attachment>("/procurement/attachments", f);
}
export const useTrace = (p: { requestId?: string; purchaseOrderId?: string }, enabled: boolean) =>
  useQuery({ queryKey: [...PROC_KEY, "trace", p], enabled, networkMode: "always", queryFn: useReportCache()("procurement:trace", qs(p), () => get<Trace>(`/procurement/trace${qs(p)}`)) });
export const useSpend = (p: Params) =>
  useQuery({ queryKey: [...PROC_KEY, "spend", p], networkMode: "always", queryFn: useReportCache()("procurement:spend", qs(p), () => get<SpendReport>(`/procurement/reports/spend${qs(p)}`)), placeholderData: keepPreviousData });
export const useSummary = (plantId: string) => useQuery({ queryKey: [...PROC_KEY, "summary", plantId], networkMode: "always", queryFn: useReportCache()("procurement:summary", plantId, () => get<Summary>(`/procurement/summary${qs({ plantId })}`)) });

/** Line value in RWF (quantity × unit price, rounded like the server). */
export const lineValue = (qty: number, price: number) => Math.round(qty * price);
