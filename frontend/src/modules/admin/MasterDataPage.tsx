import { useEffect, useMemo, useState, type FormEvent } from "react";
import { useSearchParams } from "react-router";
import { Building2, Factory, Package, Pencil, Plus, Boxes } from "lucide-react";
import { useT, type T } from "@/i18n";
import { Alert, Badge, Button, Card, Checkbox, DataTable, EmptyState, ErrorState, Field, Input, Modal, PageHeader, Select, StatusBadge, Tabs, Textarea, type Column } from "@/components/ui";
import { useLookups, type RefItem } from "@/hooks/useLookups";
import { useAuth } from "@/auth/AuthProvider";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { formatInt, formatQty, formatRWF } from "@/lib/format";
import { ADMIN_KEY, saveMasterRow, useMasterData, type Customer, type MasterEntity, type MasterRow, type Material, type Plant, type Product } from "./api";
import { Dim, SearchInput } from "./components";

// ───────────────────────────── form definitions ─────────────────────────────

interface FieldDef {
  key: string;
  type?: "text" | "number" | "textarea" | "email" | "tel";
  required?: boolean;
  hint?: boolean;
  placeholder?: string;
  /** placeholder comes from the catalog (admin.md.ph.<key>) */
  ph?: boolean;
  /** suggestions shown as a datalist */
  options?: string[];
  /** grid span on wide screens */
  wide?: boolean;
  integer?: boolean;
  min?: number;
  upper?: boolean;
  /** STK-02: choose from this reference-data kind (unit, product_category, material_category) */
  refKind?: string;
}

interface EntityDef {
  entity: MasterEntity;
  permission: string;
  fields: FieldDef[];
  /** "Plant BTN-1" etc. for titles */
  title: (row: MasterRow) => string;
}

const DEFS: Record<MasterEntity, EntityDef> = {
  plants: {
    entity: "plants",
    permission: "masterdata.manage",
    title: (r) => (r as Plant).code,
    fields: [
      { key: "code", required: true, placeholder: "BTN-4", upper: true, hint: true },
      { key: "name", required: true, placeholder: "BTN 4 Plant" },
      { key: "location", placeholder: "Kigali, Gasabo", wide: true },
    ],
  },
  products: {
    entity: "products",
    permission: "masterdata.manage",
    title: (r) => (r as Product).name,
    fields: [
      { key: "code", required: true, placeholder: "HB20", upper: true },
      { key: "name", required: true, placeholder: "Hollow Block 20" },
      { key: "dimensions", placeholder: "20 x 20 x 40 cm" },
      { key: "unit", required: true, refKind: "unit" },
      { key: "category", refKind: "product_category" },
      { key: "defaultPrice", type: "number", required: true, integer: true, min: 0, hint: true },
      { key: "reorderLevel", type: "number", integer: true, min: 0, hint: true },
      { key: "sortOrder", type: "number", integer: true, hint: true },
    ],
  },
  materials: {
    entity: "materials",
    permission: "masterdata.manage",
    title: (r) => (r as Material).name,
    fields: [
      { key: "code", required: true, placeholder: "CEMENT", upper: true },
      { key: "name", required: true, placeholder: "Cement" },
      { key: "unit", required: true, refKind: "unit" },
      { key: "category", refKind: "material_category" },
      { key: "minLevel", type: "number", min: 0, hint: true },
      { key: "sortOrder", type: "number", integer: true, hint: true },
    ],
  },
  customers: {
    entity: "customers",
    permission: "customers.manage",
    title: (r) => (r as Customer).name,
    fields: [
      { key: "name", required: true, wide: true, upper: true, hint: true },
      { key: "tin", ph: true },
      { key: "phone", type: "tel", ph: true },
      { key: "email", type: "email", wide: true },
      { key: "address", type: "textarea", wide: true },
    ],
  },
};

const TABS: { value: MasterEntity }[] = [{ value: "plants" }, { value: "products" }, { value: "materials" }, { value: "customers" }];

// ───────────────────────────── page ─────────────────────────────

export function MasterDataPage() {
  const { t } = useT();
  const [params, setParams] = useSearchParams();
  const tab = (TABS.some((x) => x.value === params.get("tab")) ? params.get("tab") : "plants") as MasterEntity;
  const setTab = (v: MasterEntity) => setParams((p) => {
    const n = new URLSearchParams(p);
    n.set("tab", v);
    return n;
  }, { replace: true });
  const { can } = useAuth();
  const def = DEFS[tab];
  const canEdit = can(def.permission);
  const [editing, setEditing] = useState<MasterRow | "new" | null>(null);
  useEffect(() => setEditing(null), [tab]);

  return (
    <>
      <PageHeader
        title={t("admin.md.title")}
        description={t("admin.md.description")}
        breadcrumbs={[{ label: t("admin.breadcrumb") }, { label: t("admin.md.title") }]}
        actions={canEdit ? <Button icon={<Plus className="size-4" />} onClick={() => setEditing("new")}>{t(`admin.md.${def.entity}.new`)}</Button> : null}
      />
      <div className="mb-4 overflow-x-auto">
        <Tabs tabs={TABS.map((x) => ({ value: x.value, label: t(`admin.md.${x.value}.tab`) }))} value={tab} onChange={setTab} />
      </div>
      <MasterTable key={tab} def={def} canEdit={canEdit} onEdit={(r) => setEditing(r)} onCreate={() => setEditing("new")} />
      <MasterModal def={def} open={editing !== null} row={editing === "new" ? null : editing} onClose={() => setEditing(null)} />
    </>
  );
}

function ActiveCell({ active }: { active: boolean }) {
  return <StatusBadge status={active ? "ACTIVE" : "INACTIVE"} />;
}

function MasterTable({ def, canEdit, onEdit, onCreate }: { def: EntityDef; canEdit: boolean; onEdit: (r: MasterRow) => void; onCreate: () => void }) {
  const { t } = useT();
  const query = useMasterData<MasterRow>(def.entity);
  const [search, setSearch] = useState("");
  const [showInactive, setShowInactive] = useState(true);

  const rows = useMemo(() => {
    const s = search.trim().toLowerCase();
    return (query.data ?? []).filter((r) => (showInactive || r.isActive) && (!s || JSON.stringify(Object.values(r)).toLowerCase().includes(s)));
  }, [query.data, search, showInactive]);

  const actionCol: Column<MasterRow>[] = canEdit
    ? [{ key: "actions", header: <span className="sr-only">{t("common.actions")}</span>, align: "right", className: "w-12", cell: (r) => <Button variant="ghost" size="icon" aria-label={t("admin.md.editAria", { name: def.title(r) })} onClick={() => onEdit(r)}><Pencil className="size-4" /></Button> }]
    : [];

  const columns: Column<MasterRow>[] = (() => {
    switch (def.entity) {
      case "plants":
        return [
          { key: "code", header: t("admin.md.field.code"), cell: (r) => <Dim off={!r.isActive} className="font-semibold text-slate-900">{(r as Plant).code}</Dim> },
          { key: "name", header: t("admin.md.field.name"), cell: (r) => <Dim off={!r.isActive}>{(r as Plant).name}</Dim> },
          { key: "location", header: t("admin.md.field.location"), hideOnMobile: true, cell: (r) => <Dim off={!r.isActive}>{(r as Plant).location || "—"}</Dim> },
          { key: "active", header: t("common.status"), cell: (r) => <ActiveCell active={r.isActive} /> },
        ];
      case "products":
        return [
          { key: "code", header: t("admin.md.field.code"), cell: (r) => <Dim off={!r.isActive} className="font-mono text-[13px] font-semibold text-slate-900">{(r as Product).code}</Dim> },
          { key: "name", header: t("admin.md.field.name"), cell: (r) => <Dim off={!r.isActive}>{(r as Product).name}</Dim> },
          { key: "dimensions", header: t("admin.md.field.dimensions"), hideOnMobile: true, cell: (r) => <Dim off={!r.isActive}>{(r as Product).dimensions || "—"}</Dim> },
          { key: "unit", header: t("admin.md.field.unit"), hideOnMobile: true, cell: (r) => <Dim off={!r.isActive}>{(r as Product).unit}</Dim> },
          { key: "price", header: t("admin.md.col.defaultPrice"), align: "right", cell: (r) => <Dim off={!r.isActive}>{(r as Product).defaultPrice ? formatRWF((r as Product).defaultPrice) : <span className="text-slate-400">{t("admin.md.notSet")}</span>}</Dim> },
          { key: "sort", header: t("admin.md.col.sort"), align: "right", hideOnMobile: true, cell: (r) => <Dim off={!r.isActive}>{formatInt((r as Product).sortOrder)}</Dim> },
          { key: "active", header: t("common.status"), cell: (r) => <ActiveCell active={r.isActive} /> },
        ];
      case "materials":
        return [
          { key: "code", header: t("admin.md.field.code"), cell: (r) => <Dim off={!r.isActive} className="font-mono text-[13px] font-semibold text-slate-900">{(r as Material).code}</Dim> },
          { key: "name", header: t("admin.md.field.name"), cell: (r) => <Dim off={!r.isActive}>{(r as Material).name}</Dim> },
          { key: "unit", header: t("admin.md.field.unit"), cell: (r) => <Dim off={!r.isActive}>{(r as Material).unit}</Dim> },
          { key: "min", header: t("admin.md.col.minLevel"), align: "right", cell: (r) => <Dim off={!r.isActive}>{formatQty((r as Material).minLevel)}</Dim> },
          { key: "sort", header: t("admin.md.col.sort"), align: "right", hideOnMobile: true, cell: (r) => <Dim off={!r.isActive}>{formatInt((r as Material).sortOrder)}</Dim> },
          { key: "active", header: t("common.status"), cell: (r) => <ActiveCell active={r.isActive} /> },
        ];
      case "customers":
        return [
          {
            key: "name",
            header: t("admin.md.col.customer"),
            cell: (r) => {
              const c = r as Customer;
              return (
                <div className="min-w-0">
                  <Dim off={!c.isActive} className="block font-semibold text-slate-900">{c.name}</Dim>
                  {c.address ? <span className="block truncate text-xs text-slate-500">{c.address}</span> : null}
                </div>
              );
            },
          },
          { key: "tin", header: t("admin.md.field.tin"), cell: (r) => <Dim off={!r.isActive} className="tabular">{(r as Customer).tin || "—"}</Dim> },
          { key: "phone", header: t("admin.md.field.phone"), hideOnMobile: true, cell: (r) => <Dim off={!r.isActive} className="tabular whitespace-nowrap">{(r as Customer).phone || "—"}</Dim> },
          { key: "email", header: t("admin.md.field.email"), hideOnMobile: true, cell: (r) => <Dim off={!r.isActive}>{(r as Customer).email || "—"}</Dim> },
          { key: "active", header: t("common.status"), cell: (r) => <ActiveCell active={r.isActive} /> },
        ];
    }
  })();

  const icons = { plants: <Factory />, products: <Boxes />, materials: <Package />, customers: <Building2 /> };
  const total = query.data?.length ?? 0;
  const inactive = (query.data ?? []).filter((r) => !r.isActive).length;

  return (
    <Card>
      <div className="flex flex-wrap items-center justify-between gap-3 px-5 py-4">
        <SearchInput value={search} onChange={setSearch} placeholder={t(`admin.md.${def.entity}.search`)} className="w-full sm:w-72" />
        <div className="flex items-center gap-4">
          {inactive ? <Checkbox label={t("admin.md.showInactive", { count: inactive })} checked={showInactive} onChange={(e) => setShowInactive(e.target.checked)} /> : null}
          {query.data ? <Badge tone="slate">{t("admin.md.total", { count: total })}</Badge> : null}
        </div>
      </div>
      {def.entity === "customers" && !canEdit ? (
        <div className="px-5 pb-4"><Alert tone="blue">{t("admin.md.customersReadOnly")}</Alert></div>
      ) : null}
      {query.isError ? (
        <ErrorState error={query.error} retry={() => query.refetch()} />
      ) : !query.isLoading && !total ? (
        <EmptyState icon={icons[def.entity]} title={t(`admin.md.${def.entity}.empty`)} action={canEdit ? <Button onClick={onCreate}>{t(`admin.md.${def.entity}.new`)}</Button> : undefined} />
      ) : (
        <DataTable columns={[...columns, ...actionCol]} rows={rows} rowKey={(r) => r.id} loading={query.isLoading} empty={t(`admin.md.${def.entity}.noMatch`, { q: search })} />
      )}
    </Card>
  );
}

// ───────────────────────────── create / edit modal ─────────────────────────────

function RefSelect({ id, kind, value, required, invalid, onChange }: { id: string; kind: string; value: string; required: boolean; invalid: boolean; onChange: (v: string) => void }) {
  const { t } = useT();
  const { data: lookups } = useLookups();
  const opts = lookups?.refdata?.[kind] ?? [];
  const cur = refValue(opts, value);
  return (
    <Select id={id} value={cur} onChange={(e) => onChange(e.target.value)} invalid={invalid}>
      {!required || !value ? <option value="">{required ? t("admin.md.chooseRef") : t("admin.md.noneRef")}</option> : null}
      {opts.map((o) => <option key={o.code} value={o.code}>{o.name}{kind === "unit" && typeof o.metadata?.symbol === "string" ? ` (${o.metadata.symbol})` : ""}</option>)}
      {value && !opts.some((o) => o.code === cur) ? <option value={value}>{value}</option> : null}
    </Select>
  );
}

/** Maps a stored value (legacy "Pcs", a symbol like "kg") to its reference-data code. */
function refValue(items: RefItem[], value: string): string {
  const v = value.trim();
  const hit = items.find((o) => o.code.toUpperCase() === v.toUpperCase() || (typeof o.metadata?.symbol === "string" && o.metadata.symbol.toLowerCase() === v.toLowerCase()));
  return hit ? hit.code : v;
}

type Values = Record<string, string | boolean>;

function initialValues(def: EntityDef, row: MasterRow | null): Values {
  const v: Values = { isActive: row ? row.isActive : true };
  for (const f of def.fields) {
    const raw = row ? (row as unknown as Record<string, unknown>)[f.key] : undefined;
    v[f.key] = raw == null ? (f.key === "unit" && def.entity === "products" ? "PCS" : f.key === "sortOrder" || f.key === "reorderLevel" ? "0" : "") : String(raw);
  }
  return v;
}

function validate(def: EntityDef, v: Values, t: T): Record<string, string> {
  const e: Record<string, string> = {};
  for (const f of def.fields) {
    const s = String(v[f.key] ?? "").trim();
    if (f.required && !s) e[f.key] = t("common.required");
    else if (f.type === "number" && s) {
      const n = Number(s);
      if (!Number.isFinite(n)) e[f.key] = t("admin.md.err.number");
      else if (f.integer && !Number.isInteger(n)) e[f.key] = f.key === "defaultPrice" ? t("admin.md.err.wholeFrancs") : t("admin.md.err.wholeNumbers");
      else if (f.min != null && n < f.min) e[f.key] = t("admin.md.err.min", { min: f.min });
    } else if (f.type === "email" && s && !/^\S+@\S+\.\S+$/.test(s)) e[f.key] = t("admin.md.err.email");
  }
  return e;
}

function MasterModal({ def, open, row, onClose }: { def: EntityDef; open: boolean; row: MasterRow | null; onClose: () => void }) {
  const { t, tx } = useT();
  const [values, setValues] = useState<Values>(() => initialValues(def, row));
  const [touched, setTouched] = useState(false);
  useEffect(() => {
    if (!open) return;
    setValues(initialValues(def, row));
    setTouched(false);
  }, [open, row, def]);

  const save = useMutate((body: Record<string, unknown>) => saveMasterRow({ entity: def.entity, id: row?.id, body }), {
    success: row ? t(`admin.md.${def.entity}.updated`) : t(`admin.md.${def.entity}.created`),
    invalidate: [[...ADMIN_KEY, "masterdata", def.entity], ["lookups"]],
    onSuccess: () => onClose(),
  });

  const errors = validate(def, values, t);
  const err = (k: string) => (touched ? errors[k] : undefined) ?? fieldError(save.error, k);
  const set = (k: string, v: string | boolean) => setValues((s) => ({ ...s, [k]: v }));

  function submit(e: FormEvent) {
    e.preventDefault();
    setTouched(true);
    if (Object.keys(errors).length) return;
    const body: Record<string, unknown> = { isActive: values.isActive };
    for (const f of def.fields) {
      const s = String(values[f.key] ?? "").trim();
      body[f.key] = f.type === "number" ? (s === "" ? 0 : Number(s)) : f.upper ? s.toUpperCase() : s === "" ? null : s;
    }
    save.mutate(body);
  }

  const generalError = save.error && !def.fields.some((f) => fieldError(save.error, f.key)) ? save.error.message : null;
  return (
    <Modal
      open={open}
      onClose={onClose}
      title={row ? t(`admin.md.${def.entity}.edit`) : t(`admin.md.${def.entity}.new`)}
      description={row ? def.title(row) : undefined}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
          <Button type="submit" form="master-form" loading={save.isPending}>{row ? t("common.saveChanges") : t(`admin.md.${def.entity}.create`)}</Button>
        </>
      }
    >
      <form id="master-form" onSubmit={submit} noValidate className="grid gap-4 sm:grid-cols-2">
        {generalError ? <div className="sm:col-span-2"><Alert tone="red">{generalError}</Alert></div> : null}
        {def.fields.map((f, i) => {
          const id = `md-${f.key}`;
          const value = String(values[f.key] ?? "");
          return (
            <Field key={f.key} label={tx(`admin.md.field.${f.key}`)} required={f.required} error={err(f.key)} hint={f.hint ? tx(`admin.md.hint.${def.entity}.${f.key}`) : undefined} htmlFor={id} className={f.wide ? "sm:col-span-2" : undefined}>
              {f.refKind ? (
                <RefSelect id={id} kind={f.refKind} value={value} required={!!f.required} invalid={!!err(f.key)} onChange={(v) => set(f.key, v)} />
              ) : f.type === "textarea" ? (
                <Textarea id={id} value={value} onChange={(e) => set(f.key, e.target.value)} invalid={!!err(f.key)} rows={2} />
              ) : (
                <>
                  <Input
                    id={id}
                    type={f.type === "number" ? "number" : f.type ?? "text"}
                    inputMode={f.type === "number" ? (f.integer ? "numeric" : "decimal") : undefined}
                    step={f.type === "number" ? (f.integer ? 1 : "any") : undefined}
                    min={f.min}
                    value={value}
                    onChange={(e) => set(f.key, e.target.value)}
                    placeholder={f.ph ? tx(`admin.md.ph.${f.key}`) : f.placeholder}
                    invalid={!!err(f.key)}
                    autoFocus={i === 0}
                    list={f.options ? `${id}-list` : undefined}
                    className={f.upper ? "uppercase placeholder:normal-case" : f.type === "number" ? "tabular" : undefined}
                    autoComplete="off"
                  />
                  {f.options ? <datalist id={`${id}-list`}>{f.options.map((o) => <option key={o} value={o} />)}</datalist> : null}
                </>
              )}
            </Field>
          );
        })}
        <div className="sm:col-span-2">
          <Checkbox label={t("admin.md.active")} checked={!!values.isActive} onChange={(e) => set("isActive", e.target.checked)} />
          <p className="mt-1 text-xs text-slate-500">{t(`admin.md.${def.entity}.inactiveNote`)}</p>
        </div>
      </form>
    </Modal>
  );
}
