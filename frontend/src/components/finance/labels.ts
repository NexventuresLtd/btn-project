import { useT } from "@/i18n";

/** Translated label of a link type ("Purchase order", "Free reference (vehicle, asset …)" …); falls back to the server label. */
export function useLinkTypeLabel() {
  const { tx } = useT();
  return (type: string, fallback?: string) => tx(`finrecords.type.${type}`, undefined, fallback ?? type);
}
