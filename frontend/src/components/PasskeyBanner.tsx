import { useState } from "react";
import { Link } from "react-router";
import { Fingerprint, X } from "lucide-react";
import { useAuth } from "@/auth/AuthProvider";
import { useT } from "@/i18n";
import { usePasskeys } from "@/modules/account/api";
import { passkeyAvailability } from "@/lib/passkeys";

const key = (id: string) => `btn.passkeyBanner.dismissed.${id}`;

/** Dashboard nudge: suggests a passkey when the user has none. Dismissable (remembered per user). */
export function PasskeyBanner() {
  const { user } = useAuth();
  const { t } = useT();
  const [hidden, setHidden] = useState(() => {
    try { return !!user && localStorage.getItem(key(user.id)) === "1"; } catch { return false; }
  });
  const supported = passkeyAvailability() === null;
  const known = user?.passkeyCount;
  const { data } = usePasskeys(!!user && !hidden && supported && !user.mustChangePassword && known === undefined);
  const count = known ?? data?.length;
  if (!user || hidden || !supported || count === undefined || count > 0) return null;
  return (
    <div role="region" aria-label={t("security.banner.title")} className="mb-6 flex flex-wrap items-center gap-x-4 gap-y-3 rounded-2xl border border-navy-100 bg-navy-50/70 px-5 py-4">
      <span className="grid size-10 shrink-0 place-items-center rounded-xl bg-white text-navy-700 ring-1 ring-navy-100"><Fingerprint className="size-5" /></span>
      <div className="min-w-0 flex-1 basis-64">
        <p className="text-sm font-semibold text-navy-900">{t("security.banner.title")}</p>
        <p className="text-[13px] text-navy-800/80">{t("security.banner.text")}</p>
      </div>
      <Link to="/account/security" className="rounded-xl bg-navy-800 px-4 py-2 text-sm font-semibold text-white hover:bg-navy-700">{t("security.banner.cta")}</Link>
      <button aria-label={t("common.dismiss")} title={t("common.dismiss")} onClick={() => { try { localStorage.setItem(key(user.id), "1"); } catch { /* ignore */ } setHidden(true); }} className="rounded-lg p-1.5 text-navy-400 hover:bg-white hover:text-navy-700"><X className="size-4" /></button>
    </div>
  );
}
