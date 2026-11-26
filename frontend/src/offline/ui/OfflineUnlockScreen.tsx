import { useState } from "react";
import { CloudOff, RefreshCw } from "lucide-react";
import { useQueryClient } from "@tanstack/react-query";
import { AuthHeading, AuthLayout } from "@/layouts/AuthLayout";
import { ME_KEY } from "@/auth/AuthProvider";
import { Button } from "@/components/ui";
import { useT } from "@/i18n";
import { connectivity } from "../core/connectivity";
import { UnlockForm } from "./forms";

/**
 * Shown instead of the sign-in page when the server cannot be reached (OFF-12, AUTH-10):
 * unlock the device's encrypted data with the offline PIN (or password) within the offline window.
 */
export function OfflineUnlockScreen() {
  const { t } = useT();
  const qc = useQueryClient();
  const [checking, setChecking] = useState(false);
  const retry = async () => {
    setChecking(true);
    try {
      if (await connectivity.probe()) await qc.invalidateQueries({ queryKey: ME_KEY });
    } finally {
      setChecking(false);
    }
  };
  return (
    <AuthLayout>
      <div className="mb-4 inline-flex items-center gap-2 rounded-full bg-slate-800 px-3 py-1 text-xs font-semibold text-white"><CloudOff className="size-3.5" />{t("offline.status.offline")}</div>
      <AuthHeading title={t("offline.unlock.title")} subtitle={t("offline.unlock.subtitle")} />
      <UnlockForm />
      <div className="mt-6 flex justify-center">
        <Button variant="ghost" size="sm" loading={checking} icon={<RefreshCw className="size-3.5" />} onClick={() => void retry()}>{t("offline.unlock.checkConnection")}</Button>
      </div>
    </AuthLayout>
  );
}
