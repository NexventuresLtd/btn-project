import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { API_BASE, ensureFreshSession, get, qs, upload } from "@/lib/api";

export const IMPORT_KEY = ["dataimport"] as const;

/** Permissions that open the import page (one per kind; mirrors internal/dataimport kinds). */
export const IMPORT_PERMISSIONS = ["masterdata.manage", "catalog.manage", "customers.manage", "workers.manage", "stock.adjust", "materials.adjust"];

export interface ImportColumn {
  name: string;
  required: boolean;
  type: "text" | "code" | "integer" | "decimal" | "money" | "date" | "boolean" | "enum";
  typeLabel: string;
  description: string;
  example: string;
  values?: string[];
}

export interface ImportKind {
  kind: string;
  label: string;
  description: string;
  permission: string;
  columns: ImportColumn[];
}

export interface KindsResponse {
  items: ImportKind[];
  limits: { maxBytes: number; maxRows: number };
}

export type RowAction = "create" | "update" | "unchanged" | "error";

export interface RowIssue {
  field: string;
  message: string;
}

export interface ImportItem {
  row: number;
  action: RowAction;
  key: string;
  label?: string;
  changes?: string[];
  errors: RowIssue[];
  warnings: RowIssue[];
}

export interface ImportSummary {
  total: number;
  create: number;
  update: number;
  unchanged: number;
  errors: number;
  warnings: number;
}

export interface ImportReport {
  id: string;
  kind: string;
  fileName: string;
  fileHash: string;
  rows: number;
  summary: ImportSummary;
  items: ImportItem[];
  warnings: string[];
  committed: boolean;
  createdAt: string;
}

export interface HistoryRow {
  id: string;
  kind: string;
  kindLabel: string;
  fileName: string;
  fileHash: string;
  rows: number;
  summary: ImportSummary;
  committed: boolean;
  createdAt: string;
  user: { id: string; name: string } | null;
}

export interface Paged<T> {
  items: T[];
  total: number;
  page: number;
  pageSize: number;
}

export function useImportKinds() {
  return useQuery({
    queryKey: [...IMPORT_KEY, "kinds"],
    queryFn: ({ signal }) => get<KindsResponse>("/data/import/kinds", signal),
    staleTime: 5 * 60_000,
  });
}

export function useImportHistory(page: number) {
  return useQuery({
    queryKey: [...IMPORT_KEY, "history", page],
    queryFn: ({ signal }) => get<Paged<HistoryRow>>(`/data/import/history${qs({ page, pageSize: 10 })}`, signal),
    placeholderData: keepPreviousData,
  });
}

function form(file: File) {
  const f = new FormData();
  f.append("file", file, file.name);
  return f;
}

/** Dry run: validates every row, writes nothing (the report is kept in the history). */
export const validateFile = ({ kind, file }: { kind: string; file: File }) => upload<ImportReport>(`/data/import/${kind}/validate`, form(file));

/** Re-validates and applies every row in one transaction (422 when any row has an error). */
export const commitFile = ({ kind, file }: { kind: string; file: File }) => upload<ImportReport>(`/data/import/${kind}/commit`, form(file));

/** Same prefix the @/lib/api helpers use for requests. */
const API_PREFIX = API_BASE;

/** Downloads a server-generated file; the session cookie authenticates the plain link. */
async function download(path: string) {
  await ensureFreshSession().catch(() => undefined);
  const a = document.createElement("a");
  a.href = `${API_PREFIX}${path}`;
  a.download = "";
  a.rel = "noopener";
  document.body.appendChild(a);
  a.click();
  a.remove();
}

export const downloadTemplate = (kind: string) => download(`/data/import/templates/${kind}`);
export const downloadReport = (id: string) => download(`/data/import/reports/${id}?format=csv`);
