import { Badge, StatusBadge } from "@/components/ui";
import { useT } from "@/i18n";
import type { PayrollStatus } from "./api";

/** Wage sheet status; PAID reads "Payments recorded" (wages are paid outside the system and recorded here). */
export function PayrollStatusBadge({ status, className }: { status: PayrollStatus; className?: string }) {
  const { t } = useT();
  if (status === "PAID") return <Badge tone="navy" dot className={className}>{t("payroll.status.PAID")}</Badge>;
  return <StatusBadge status={status} className={className} />;
}
