import { Link } from "react-router";
import { EmptyState } from "@/components/ui";
import { useT } from "@/i18n";

export function NotFound() {
  const { t } = useT();
  return <EmptyState title={t("common.notFoundTitle")} description={t("common.notFoundText")} action={<Link to="/" className="font-semibold text-navy-700">{t("common.goDashboard")}</Link>} />;
}
