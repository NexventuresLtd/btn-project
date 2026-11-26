import type { ReactNode } from "react";
import { Navigate, useLocation } from "react-router";
import { ShieldAlert } from "lucide-react";
import { useAuth } from "./AuthProvider";
import { EmptyState, PageLoader } from "@/components/ui";
import { useT } from "@/i18n";
import { OfflineUnlockScreen } from "@/offline/ui/OfflineUnlockScreen";

export function RequireAuth({ children }: { children: ReactNode }) {
  const { user, loading, offline } = useAuth();
  const location = useLocation();
  if (loading) return <PageLoader />;
  if (!user && offline) return <OfflineUnlockScreen />;
  if (!user) return <Navigate to={`/login?next=${encodeURIComponent(location.pathname + location.search)}`} replace />;
  if (user.mustChangePassword && location.pathname !== "/account/password") return <Navigate to="/account/password" replace />;
  return <>{children}</>;
}

/** Hides a page from users without the permission (the API enforces it too). */
export function RequirePermission({ permission, children }: { permission: string | string[]; children: ReactNode }) {
  const { can } = useAuth();
  const { t } = useT();
  const perms = Array.isArray(permission) ? permission : [permission];
  if (!perms.some(can)) {
    return <EmptyState icon={<ShieldAlert />} title={t("common.accessTitle")} description={t("common.accessText")} />;
  }
  return <>{children}</>;
}

/** Renders children only when the user has the permission. */
export function Can({ permission, children }: { permission: string; children: ReactNode }) {
  const { can } = useAuth();
  return can(permission) ? <>{children}</> : null;
}
