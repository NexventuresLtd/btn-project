import { useState } from "react";
import { Download } from "lucide-react";
import { useT } from "@/i18n";
import { Alert, Button } from "@/components/ui";
import { useConnectivity, useOfflineState } from "../hooks";
import { applyPendingUpdate } from "../runtime";

/** Fetches the newest app version (service worker update) and reloads; pending records stay in IndexedDB. */
export async function updateApp() {
  try {
    const reg = await navigator.serviceWorker?.getRegistration();
    await reg?.update();
  } catch { /* offline or no service worker: a plain reload still picks up a new build */ }
  location.reload();
}

/**
 * §9.2: the server no longer accepts this app's sync schema version (426 schema_upgrade_required). Sync is paused
 * until the app is updated; nothing captured on the device is lost or rejected.
 */
export function SchemaUpgradeNotice({ compact = false }: { compact?: boolean }) {
  const { t } = useT();
  const c = useConnectivity();
  const st = useOfflineState();
  const [busy, setBusy] = useState(false);
  if (c.blocked !== "upgrade") return null;
  const update = () => {
    if (st.updateReady && !st.dirtyForms) return applyPendingUpdate();
    setBusy(true);
    void updateApp();
  };
  const button = (
    <Button size="sm" icon={<Download className="size-3.5" />} loading={busy} disabled={st.dirtyForms > 0} onClick={update} data-testid="schema-update">
      {t("syncschema.update")}
    </Button>
  );
  if (compact) {
    return (
      <div className="space-y-2" data-sync-blocked="upgrade">
        <p className="text-xs font-medium text-amber-800">{t("syncschema.title")}</p>
        <p className="text-xs text-slate-600">{t("syncschema.text")}</p>
        {button}
      </div>
    );
  }
  return (
    <div className="mb-6" data-sync-blocked="upgrade">
      <Alert tone="amber" title={t("syncschema.title")}>
        <p>{st.dirtyForms ? t("syncschema.waitForms") : t("syncschema.text")}</p>
        <div className="mt-2">{button}</div>
      </Alert>
    </div>
  );
}
