import { useEffect, useState, type FormEvent } from "react";
import { Link } from "react-router";
import { Building2, MapPin, Pencil, Plus, Power, Users } from "lucide-react";
import { useT, type T } from "@/i18n";
import { Alert, Button, Card, Checkbox, EmptyState, ErrorState, Field, Input, Modal, PageHeader, Spinner, StatusBadge } from "@/components/ui";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { formatInt } from "@/lib/format";
import { ADMIN_KEY, saveMasterRow, usePlantStats, type PlantStat } from "./api";

const INVALIDATE = [[...ADMIN_KEY, "plantStats"], [...ADMIN_KEY, "masterdata", "plants"], ["lookups"]];

export function SitesPage() {
  const { t } = useT();
  const query = usePlantStats();
  const [editing, setEditing] = useState<PlantStat | "new" | null>(null);

  const toggle = useMutate((s: PlantStat) => saveMasterRow({ entity: "plants", id: s.plantId, body: { isActive: !s.isActive } }), {
    success: t("admin.sites.updated"),
    invalidate: INVALIDATE,
  });

  const sites = query.data ?? [];

  return (
    <>
      <PageHeader
        title={t("admin.sites.title")}
        description={t("admin.sites.description")}
        breadcrumbs={[{ label: t("admin.breadcrumb") }, { label: t("admin.sites.title") }]}
        actions={<Button icon={<Plus className="size-4" />} onClick={() => setEditing("new")}>{t("admin.sites.new")}</Button>}
      />

      <div className="mb-5"><Alert tone="blue">{t("admin.sites.helper")}</Alert></div>

      {query.isError ? (
        <Card><ErrorState error={query.error} retry={() => query.refetch()} /></Card>
      ) : query.isLoading && !query.data ? (
        <Card><div className="flex justify-center py-16"><Spinner /></div></Card>
      ) : sites.length === 0 ? (
        <Card><EmptyState icon={<Building2 />} title={t("admin.sites.empty")} action={<Button onClick={() => setEditing("new")}>{t("admin.sites.new")}</Button>} /></Card>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
          {sites.map((s) => (
            <Card key={s.plantId} className="flex flex-col p-5">
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="font-mono text-base font-bold text-slate-900">{s.code}</span>
                    <StatusBadge status={s.isActive ? "ACTIVE" : "INACTIVE"} />
                  </div>
                  <p className="mt-0.5 truncate text-sm text-slate-600">{s.name}</p>
                </div>
                <span className="grid size-10 shrink-0 place-items-center rounded-xl bg-navy-50 text-navy-700"><Building2 className="size-5" /></span>
              </div>

              <p className="mt-3 flex items-center gap-1.5 text-sm text-slate-500">
                <MapPin className="size-4 shrink-0 text-slate-400" />
                {s.location || <span className="text-slate-400">—</span>}
              </p>

              <div className="mt-4 grid grid-cols-2 gap-3 border-t border-line pt-4">
                <Stat icon={<Users className="size-4" />} value={formatInt(s.workerCount)} label={t("admin.sites.workers", { count: s.workerCount })} />
                <Stat icon={<Users className="size-4" />} value={formatInt(s.userCount)} label={t("admin.sites.users", { count: s.userCount })} tone="navy" />
              </div>

              <div className="mt-4 flex items-center gap-2 border-t border-line pt-4">
                <Button variant="secondary" size="sm" icon={<Pencil className="size-3.5" />} onClick={() => setEditing(s)}>{t("admin.sites.edit")}</Button>
                <Button
                  variant="ghost"
                  size="sm"
                  icon={<Power className="size-3.5" />}
                  loading={toggle.isPending && toggle.variables?.plantId === s.plantId}
                  onClick={() => toggle.mutate(s)}
                  className={s.isActive ? "text-red-600 hover:bg-red-50" : "text-emerald-600 hover:bg-emerald-50"}
                >
                  {s.isActive ? t("admin.sites.inactive") : t("admin.sites.active")}
                </Button>
                {s.workerCount > 0 ? (
                  <Link to={`/payroll/workers?plantId=${s.plantId}`} className="ml-auto text-xs font-semibold text-navy-700 hover:underline">{t("admin.sites.workers", { count: s.workerCount })}</Link>
                ) : null}
              </div>
            </Card>
          ))}
        </div>
      )}

      <SiteModal open={editing !== null} site={editing === "new" ? null : editing} onClose={() => setEditing(null)} />
    </>
  );
}

function Stat({ icon, value, label, tone = "slate" }: { icon: React.ReactNode; value: string; label: string; tone?: "slate" | "navy" }) {
  return (
    <div className="flex items-center gap-2.5">
      <span className={`grid size-9 place-items-center rounded-lg ${tone === "navy" ? "bg-navy-50 text-navy-700" : "bg-slate-100 text-slate-600"}`}>{icon}</span>
      <div className="min-w-0">
        <p className="tabular text-lg leading-none font-bold text-slate-900">{value}</p>
        <p className="truncate text-[11px] text-slate-500">{label}</p>
      </div>
    </div>
  );
}

// ───────────────────────────── add / edit modal ─────────────────────────────

interface Values { code: string; name: string; location: string; isActive: boolean }

function initial(site: PlantStat | null): Values {
  return { code: site?.code ?? "", name: site?.name ?? "", location: site?.location ?? "", isActive: site ? site.isActive : true };
}

function validate(v: Values, t: T): Record<string, string> {
  const e: Record<string, string> = {};
  if (!v.code.trim()) e.code = t("common.required");
  if (!v.name.trim()) e.name = t("common.required");
  return e;
}

function SiteModal({ open, site, onClose }: { open: boolean; site: PlantStat | null; onClose: () => void }) {
  const { t } = useT();
  const [values, setValues] = useState<Values>(() => initial(site));
  const [touched, setTouched] = useState(false);
  useEffect(() => {
    if (!open) return;
    setValues(initial(site));
    setTouched(false);
  }, [open, site]);

  const save = useMutate((body: Record<string, unknown>) => saveMasterRow({ entity: "plants", id: site?.plantId, body }), {
    success: site ? t("admin.sites.updated") : t("admin.sites.created"),
    invalidate: INVALIDATE,
    onSuccess: () => onClose(),
  });

  const errors = validate(values, t);
  const err = (k: string) => (touched ? errors[k] : undefined) ?? fieldError(save.error, k);
  const set = (k: keyof Values, val: string | boolean) => setValues((s) => ({ ...s, [k]: val }));

  function submit(e: FormEvent) {
    e.preventDefault();
    setTouched(true);
    if (Object.keys(errors).length) return;
    save.mutate({ code: values.code.trim().toUpperCase(), name: values.name.trim(), location: values.location.trim() || null, isActive: values.isActive });
  }

  const generalError = save.error && !["code", "name", "location"].some((k) => fieldError(save.error, k)) ? save.error.message : null;

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={site ? t("admin.sites.edit") : t("admin.sites.new")}
      description={site?.code}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
          <Button type="submit" form="site-form" loading={save.isPending}>{site ? t("common.saveChanges") : t("admin.sites.create")}</Button>
        </>
      }
    >
      <form id="site-form" onSubmit={submit} noValidate className="grid gap-4 sm:grid-cols-2">
        {generalError ? <div className="sm:col-span-2"><Alert tone="red">{generalError}</Alert></div> : null}
        <Field label={t("admin.sites.code")} required error={err("code")} hint={t("admin.sites.codeHint")} htmlFor="site-code">
          <Input id="site-code" value={values.code} onChange={(e) => set("code", e.target.value)} placeholder={t("admin.sites.codePlaceholder")} invalid={!!err("code")} autoFocus className="uppercase placeholder:normal-case" autoComplete="off" />
        </Field>
        <Field label={t("admin.sites.name")} required error={err("name")} htmlFor="site-name">
          <Input id="site-name" value={values.name} onChange={(e) => set("name", e.target.value)} placeholder={t("admin.sites.namePlaceholder")} invalid={!!err("name")} autoComplete="off" />
        </Field>
        <Field label={t("admin.sites.location")} error={err("location")} htmlFor="site-location" className="sm:col-span-2">
          <Input id="site-location" value={values.location} onChange={(e) => set("location", e.target.value)} placeholder={t("admin.sites.locationPlaceholder")} invalid={!!err("location")} autoComplete="off" />
        </Field>
        <div className="sm:col-span-2">
          <Checkbox label={t("admin.sites.activeLabel")} checked={values.isActive} onChange={(e) => set("isActive", e.target.checked)} />
          <p className="mt-1 text-xs text-slate-500">{t("admin.sites.inactiveNote")}</p>
        </div>
      </form>
    </Modal>
  );
}
