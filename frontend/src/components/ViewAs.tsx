import { useEffect, useState, type FormEvent } from "react";
import { useNavigate } from "react-router";
import { Eye, X } from "lucide-react";
import { toast } from "sonner";
import { useAuth } from "@/auth/AuthProvider";
import { useT } from "@/i18n";
import { ApiError } from "@/lib/api";
import { OPTIONALLY_SCOPED_ROLES, PLANT_SCOPED_ROLES } from "@/lib/scope";
import { sitesOnly, useLookups } from "@/hooks/useLookups";
import { Button, Field, Modal, Select } from "@/components/ui";

/** Every role a Super Administrator can preview, managers first. */
const VIEW_AS_ROLES = ["MANAGING_DIRECTOR", "GENERAL_MANAGER", "OPERATIONS_ADVISOR", "PRODUCTION_MANAGER", "SALES_MANAGER", "FINANCE_MANAGER",
  "PROCUREMENT_OFFICER", "STOCK_OFFICER", "SUPERVISOR", "LEAD_OPERATOR", "OPERATOR", "SYSTEM_ADMIN"];

/** Pick a role (and, for site roles, a site) to preview the app as. */
export function ViewAsDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { t, tx } = useT();
  const { user, startViewAs } = useAuth();
  const navigate = useNavigate();
  const lookups = useLookups();
  const [role, setRole] = useState("");
  const [plantId, setPlantId] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (open) {
      setRole(user?.viewAs ? user.role : "");
      setPlantId(user?.viewAs ? user.plant?.id ?? "" : "");
    }
  }, [open, user]);
  const needsSite = PLANT_SCOPED_ROLES.includes(role);
  const mayHaveSite = needsSite || OPTIONALLY_SCOPED_ROLES.includes(role);
  const sites = needsSite ? sitesOnly(lookups.data?.plants) : lookups.data?.plants ?? [];
  const ready = role !== "" && (!needsSite || plantId !== "");

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!ready) return;
    setBusy(true);
    try {
      await startViewAs(role, mayHaveSite ? plantId : undefined);
      onClose();
      navigate("/"); // the current page may not exist for that role
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : t("layout.viewAs.failed"));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal open={open} onClose={onClose} title={t("layout.viewAs.title")} description={t("layout.viewAs.hint")} size="sm" footer={
      <>
        <Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
        <Button type="submit" form="view-as-form" icon={<Eye className="size-4" />} loading={busy} disabled={!ready}>{t("layout.viewAs.start")}</Button>
      </>
    }>
      <form id="view-as-form" onSubmit={submit} className="space-y-4">
        <Field label={t("layout.viewAs.role")} htmlFor="view-as-role" required hint={role ? tx(`enums.roleDesc.${role}`, undefined, "") : undefined}>
          <Select id="view-as-role" value={role} onChange={(e) => { setRole(e.target.value); setPlantId(""); }} autoFocus>
            <option value="" disabled>{t("layout.viewAs.pickRole")}</option>
            {VIEW_AS_ROLES.map((r) => <option key={r} value={r}>{tx(`enums.role.${r}`, undefined, r)}</option>)}
          </Select>
        </Field>
        {mayHaveSite ? (
          <Field label={t("layout.viewAs.site")} htmlFor="view-as-site" required={needsSite}>
            <Select id="view-as-site" value={plantId} onChange={(e) => setPlantId(e.target.value)}>
              <option value="" disabled={needsSite}>{needsSite ? t("layout.viewAs.pickSite") : t("layout.viewAs.allSites")}</option>
              {sites.map((p) => <option key={p.id} value={p.id}>{p.code} — {p.name}</option>)}
            </Select>
          </Field>
        ) : null}
      </form>
    </Modal>
  );
}

/** Shown across the top while previewing: which role, which site, read-only, switch / exit. */
export function ViewAsBar() {
  const { t, tx } = useT();
  const { user, exitViewAs } = useAuth();
  const navigate = useNavigate();
  const [picking, setPicking] = useState(false);
  const [busy, setBusy] = useState(false);
  if (!user?.viewAs) return null;
  const exit = async () => {
    setBusy(true);
    try {
      await exitViewAs();
      navigate("/");
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : t("layout.viewAs.failed"));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div role="status" className="sticky top-0 z-30 flex flex-wrap items-center gap-x-3 gap-y-1.5 bg-amber-400 px-4 py-2 text-[13px] text-amber-950 sm:px-8">
      <Eye className="size-4 shrink-0" aria-hidden />
      <p className="min-w-0 flex-1">
        <span className="font-semibold">{t("layout.viewAs.bar", { role: tx(`enums.role.${user.role}`, undefined, user.roleLabel) })}</span>
        {user.plant ? <span> · {user.plant.code}</span> : null}
        <span className="hidden sm:inline"> — {t("layout.viewAs.readOnly")}</span>
      </p>
      <button onClick={() => setPicking(true)} className="rounded-lg px-2.5 py-1 font-medium hover:bg-amber-300">{t("layout.viewAs.switch")}</button>
      <button onClick={() => void exit()} disabled={busy} className="inline-flex items-center gap-1 rounded-lg bg-amber-950 px-2.5 py-1 font-medium text-amber-50 hover:bg-amber-900 disabled:opacity-60">
        <X className="size-3.5" aria-hidden /> {t("layout.viewAs.exit")}
      </button>
      <ViewAsDialog open={picking} onClose={() => setPicking(false)} />
    </div>
  );
}
