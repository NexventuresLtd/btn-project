import { useQuery } from "@tanstack/react-query";
import { get, qs } from "@/lib/api";
import { getLang } from "@/i18n";

export const DATAEXPORT_KEY = ["dataexport"] as const;

export interface ExportTable {
  name: string;
  label: string;
  rows: number;
  personal: boolean;
  dateFilter: boolean;
  columns: string[];
}

export type BulkFormat = "csv" | "json";

export function useExportTables() {
  return useQuery({
    queryKey: [...DATAEXPORT_KEY, "tables", getLang()],
    queryFn: ({ signal }) => get<{ items: ExportTable[]; formats: BulkFormat[] }>("/data/export/tables", signal),
  });
}

/** Paths for GET /api/v1/data/export/… (streamed by the server; audited). */
export const tablePath = (name: string, format: BulkFormat, range: { from?: string; to?: string }) =>
  `/data/export/tables/${encodeURIComponent(name)}${qs({ format, lang: getLang(), ...range })}`;
export const allPath = (format: BulkFormat, range: { from?: string; to?: string }) => `/data/export/all${qs({ format, lang: getLang(), ...range })}`;
