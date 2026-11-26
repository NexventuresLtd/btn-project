/** Record types that can be captured offline. Phase-2 modules register theirs at import time. */
export interface OfflineRecordType<P = unknown, L = unknown> {
  /** sync type sent to the server (matches the server's syncsvc handler name) */
  type: string;
  /** payload schema version; bump when the payload shape changes and add `migrate` */
  schemaVersion: number;
  /** domain key of the local record this event creates/updates (e.g. "plantId|date|workerId"); default = uuid */
  key?: (payload: P) => string;
  /** business date (YYYY-MM-DD) for the 60-day local scope */
  date?: (payload: P) => string | null;
  /** local record kept for lists/forms (default: the payload) */
  toLocal?: (payload: P, previous: L | null) => L;
  /** permission(s) needed to capture (ANY of); checked before saving so offline users are not surprised by a rejection later */
  permission?: string | string[];
  /** client-side validation before saving; return a translated message or null */
  validate?: (payload: P) => string | null;
  /** upgrade an outbox payload captured by an older app version */
  migrate?: (payload: unknown, fromVersion: number) => P;
  /** one-line human summary for the sync centre (already translated) */
  describe?: (payload: P) => string;
  /** fields the user may correct before resubmitting (default: every top-level text/number field) */
  fields?: EditField[];
  /** TanStack Query keys to refresh after records of this type synced */
  invalidate?: readonly (readonly unknown[])[];
  /** delta-pull entity that refreshes these local records from the server */
  pull?: { entity: string; key: (data: Record<string, unknown>) => string; date?: (data: Record<string, unknown>) => string | null; toLocal?: (data: Record<string, unknown>) => L };
}

export interface EditField {
  name: string;
  kind: "text" | "number" | "date" | "select";
  /** select options: value + translated label */
  options?: () => { value: string; label: string }[];
  /** translated label (default: the field name) */
  label?: () => string;
}

const types = new Map<string, OfflineRecordType<any, any>>(); // eslint-disable-line @typescript-eslint/no-explicit-any

export function registerOfflineRecordType<P, L = P>(def: OfflineRecordType<P, L>): OfflineRecordType<P, L> {
  types.set(def.type, def);
  return def;
}

export const getRecordType = (type: string) => types.get(type);
export const recordTypes = () => [...types.values()];
export const pullEntityType = (entity: string) => [...types.values()].find((t) => t.pull?.entity === entity);
