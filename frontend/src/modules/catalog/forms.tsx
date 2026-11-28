import { useEffect, useState, type FormEvent } from "react";
import { Pencil, Plus, Trash2 } from "lucide-react";
import { useT, type Lang, type T } from "@/i18n";
import { Alert, Badge, Button, Checkbox, EmptyState, ErrorState, Field, Input, Modal, RemoveDialog, Select, Spinner } from "@/components/ui";
import { useLookups, type RefItem } from "@/hooks/useLookups";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { RemovedList } from "@/modules/admin/RemovedList";
import { removeMasterRow, restoreMasterRow, saveMasterRow, useRemovedMaster, type Material, type Product } from "@/modules/admin/api";
import { createRef, removeRef, restoreRef, updateRef, useRefData, useRemovedRef, type RefKind } from "@/modules/admin/refdataApi";
import { catalogInvalidate, REF_INVALIDATE } from "./api";
import { codeFromName } from "./helpers";

export type CatalogEntity = "products" | "materials";
type Row = Product | Material;

// ───────────────────────────── product / material form ─────────────────────────────

interface FieldDef {
  key: string;
  type?: "text" | "number";
  required?: boolean;
  hint?: boolean;
  placeholder?: string;
  integer?: boolean;
  min?: number;
  upper?: boolean;
  /** choose from this reference-data kind (unit, product_category, material_category) */
  refKind?: string;
}

const FIELDS: Record<CatalogEntity, FieldDef[]> = {
  products: [
    { key: "name", required: true, placeholder: "Hollow Block 20" },
    { key: "code", required: true, placeholder: "HB20", upper: true },
    { key: "category", refKind: "product_category" },
    { key: "unit", required: true, refKind: "unit" },
    { key: "defaultPrice", type: "number", required: true, integer: true, min: 0, hint: true },
    { key: "dimensions", placeholder: "20 x 20 x 40 cm" },
    { key: "reorderLevel", type: "number", integer: true, min: 0, hint: true },
    { key: "sortOrder", type: "number", integer: true, hint: true },
  ],
  materials: [
    { key: "name", required: true, placeholder: "Cement" },
    { key: "code", required: true, placeholder: "CEMENT", upper: true },
    { key: "category", refKind: "material_category" },
    { key: "unit", required: true, refKind: "unit" },
    { key: "minLevel", type: "number", min: 0, hint: true },
    { key: "sortOrder", type: "number", integer: true, hint: true },
  ],
};

type Values = Record<string, string | boolean>;

function initialValues(entity: CatalogEntity, row: Row | null): Values {
  const v: Values = { isActive: row ? row.isActive : true };
  for (const f of FIELDS[entity]) {
    const raw = row ? (row as unknown as Record<string, unknown>)[f.key] : undefined;
    v[f.key] = raw == null ? (f.key === "unit" && entity === "products" ? "PCS" : f.key === "sortOrder" || f.key === "reorderLevel" ? "0" : "") : String(raw);
  }
  return v;
}

/** Same rules as the API (and the former Master data page). Exported for tests. */
export function validateCatalog(entity: CatalogEntity, v: Values, t: T): Record<string, string> {
  const e: Record<string, string> = {};
  for (const f of FIELDS[entity]) {
    const s = String(v[f.key] ?? "").trim();
    if (f.required && !s) e[f.key] = t("common.required");
    else if (f.type === "number" && s) {
      const n = Number(s);
      if (!Number.isFinite(n)) e[f.key] = t("admin.md.err.number");
      else if (f.integer && !Number.isInteger(n)) e[f.key] = f.key === "defaultPrice" ? t("admin.md.err.wholeFrancs") : t("admin.md.err.wholeNumbers");
      else if (f.min != null && n < f.min) e[f.key] = t("admin.md.err.min", { min: f.min });
    }
  }
  return e;
}

/** Maps a stored value (legacy "Pcs", a symbol like "kg") to its reference-data code. */
function refValue(items: RefItem[], value: string): string {
  const v = value.trim();
  const hit = items.find((o) => o.code.toUpperCase() === v.toUpperCase() || (typeof o.metadata?.symbol === "string" && o.metadata.symbol.toLowerCase() === v.toLowerCase()));
  return hit ? hit.code : v;
}

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

/** Add / edit a product or a material; edit also offers Remove (with a reason). */
export function CatalogItemModal({ entity, open, row, onClose, onRemoved }: { entity: CatalogEntity; open: boolean; row: Row | null; onClose: () => void; onRemoved?: () => void }) {
  const { t, tx } = useT();
  const fields = FIELDS[entity];
  const [values, setValues] = useState<Values>(() => initialValues(entity, row));
  const [touched, setTouched] = useState(false);
  const [removing, setRemoving] = useState(false);
  useEffect(() => {
    if (!open) return;
    setValues(initialValues(entity, row));
    setTouched(false);
    setRemoving(false);
  }, [open, row, entity]);

  const save = useMutate((body: Record<string, unknown>) => saveMasterRow({ entity, id: row?.id, body }), {
    success: row ? t(`admin.md.${entity}.updated`) : t(`admin.md.${entity}.created`),
    invalidate: catalogInvalidate(entity),
    onSuccess: () => onClose(),
  });
  const errors = validateCatalog(entity, values, t);
  const err = (k: string) => (touched ? errors[k] : undefined) ?? fieldError(save.error, k);
  const set = (k: string, v: string | boolean) => setValues((s) => ({ ...s, [k]: v }));

  function submit(e: FormEvent) {
    e.preventDefault();
    setTouched(true);
    if (Object.keys(errors).length) return;
    const body: Record<string, unknown> = { isActive: values.isActive };
    for (const f of fields) {
      const s = String(values[f.key] ?? "").trim();
      body[f.key] = f.type === "number" ? (s === "" ? 0 : Number(s)) : f.upper ? s.toUpperCase() : s === "" ? null : s;
    }
    save.mutate(body);
  }

  const generalError = save.error && !fields.some((f) => fieldError(save.error, f.key)) ? save.error.message : null;
  const formId = `catalog-${entity}-form`;
  return (
    <>
      <Modal
        open={open}
        onClose={onClose}
        title={row ? t(`admin.md.${entity}.edit`) : t(`admin.md.${entity}.new`)}
        description={row ? row.name : undefined}
        footer={
          <>
            {row ? <Button variant="ghost" icon={<Trash2 className="size-4" />} className="mr-auto text-red-600 hover:bg-red-50" onClick={() => setRemoving(true)}>{t("records.remove")}</Button> : null}
            <Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
            <Button type="submit" form={formId} loading={save.isPending}>{row ? t("common.saveChanges") : t(`admin.md.${entity}.create`)}</Button>
          </>
        }
      >
        <form id={formId} onSubmit={submit} noValidate className="grid gap-4 sm:grid-cols-2">
          {generalError ? <div className="sm:col-span-2"><Alert tone="red">{generalError}</Alert></div> : null}
          {fields.map((f, i) => {
            const id = `cat-${entity}-${f.key}`;
            const value = String(values[f.key] ?? "");
            return (
              <Field key={f.key} label={tx(`admin.md.field.${f.key}`)} required={f.required} error={err(f.key)} hint={f.hint ? tx(`admin.md.hint.${entity}.${f.key}`) : undefined} htmlFor={id}>
                {f.refKind ? (
                  <RefSelect id={id} kind={f.refKind} value={value} required={!!f.required} invalid={!!err(f.key)} onChange={(v) => set(f.key, v)} />
                ) : (
                  <Input
                    id={id}
                    type={f.type === "number" ? "number" : "text"}
                    inputMode={f.type === "number" ? (f.integer ? "numeric" : "decimal") : undefined}
                    step={f.type === "number" ? (f.integer ? 1 : "any") : undefined}
                    min={f.min}
                    value={value}
                    onChange={(e) => set(f.key, e.target.value)}
                    placeholder={f.placeholder}
                    invalid={!!err(f.key)}
                    autoFocus={i === 0}
                    className={f.upper ? "uppercase placeholder:normal-case" : f.type === "number" ? "tabular" : undefined}
                    autoComplete="off"
                  />
                )}
              </Field>
            );
          })}
          <div className="sm:col-span-2">
            <Checkbox label={t("admin.md.active")} checked={!!values.isActive} onChange={(e) => set("isActive", e.target.checked)} />
            <p className="mt-1 text-xs text-slate-500">{t(`admin.md.${entity}.inactiveNote`)}</p>
          </div>
        </form>
      </Modal>
      <RemoveCatalogDialog
        entity={entity}
        row={removing ? row : null}
        onClose={() => setRemoving(false)}
        onRemoved={() => {
          setRemoving(false);
          onClose();
          onRemoved?.();
        }}
      />
    </>
  );
}

/** Remove a product / material with a reason (SRS §6.3: kept, hidden from lists and dropdowns, restorable). */
export function RemoveCatalogDialog({ entity, row, onClose, onRemoved }: { entity: CatalogEntity; row: { id: string; name: string } | null; onClose: () => void; onRemoved?: () => void }) {
  const { t } = useT();
  const remove = useMutate((v: { id: string; reason: string }) => removeMasterRow({ entity, ...v }), {
    success: t("records.removedDone"),
    invalidate: catalogInvalidate(entity),
    onSuccess: () => onRemoved?.(),
  });
  return (
    <RemoveDialog
      open={!!row}
      onClose={onClose}
      onConfirm={(reason) => row && remove.mutate({ id: row.id, reason })}
      title={t("records.removeTitle", { name: row?.name ?? "" })}
      message={t("records.removeMasterMsg")}
      loading={remove.isPending}
      error={remove.error}
      onlineOnly
    />
  );
}

// ───────────────────────────── removed products / materials ─────────────────────────────

export function RemovedCatalogModal({ entity, open, onClose }: { entity: CatalogEntity; open: boolean; onClose: () => void }) {
  const { t } = useT();
  const removed = useRemovedMaster(entity);
  const restore = useMutate((id: string) => restoreMasterRow({ entity, id }), {
    success: t("records.restored"),
    invalidate: catalogInvalidate(entity),
  });
  return (
    <Modal open={open} onClose={onClose} size="lg" title={t(`catalog.removed.${entity}`)}>
      {removed.isError ? (
        <ErrorState error={removed.error} retry={() => removed.refetch()} />
      ) : removed.isLoading ? (
        <div className="py-10"><Spinner className="mx-auto" /></div>
      ) : !removed.data?.length ? (
        <EmptyState title={t("catalog.removed.none")} />
      ) : (
        <RemovedList items={removed.data} alwaysOpen onRestore={(r) => restore.mutate(r.id)} restoringId={restore.isPending ? restore.variables : undefined} className="" />
      )}
    </Modal>
  );
}

// ───────────────────────────── categories / units (reference data) ─────────────────────────────

/** The reference-data lists managed from the Products / Materials pages. */
export type CatalogRefKind = Extract<RefKind, "product_category" | "material_category" | "unit">;

const LANGS: Lang[] = ["en", "am", "rw", "fr"];
const CODE_RE = /^[A-Z0-9][A-Z0-9_-]{0,39}$/;

interface RefForm { code: string; codeEdited: boolean; names: Record<Lang, string>; symbol: string }

/**
 * Manage one reference-data list from the ⋯ menu (product / material categories, units): add, rename, remove with a
 * reason, restore. Every change refreshes the look-ups, so the category / unit selects update at once.
 */
export function RefKindModal({ kind: k, open, onClose }: { kind: CatalogRefKind; open: boolean; onClose: () => void }) {
  const { t } = useT();
  const query = useRefData(k, true);
  const removed = useRemovedRef(k);
  const [editing, setEditing] = useState<RefItem | "new" | null>(null);
  const [removing, setRemoving] = useState<RefItem | null>(null);
  const remove = useMutate((v: { id: string; reason: string }) => removeRef({ kind: k, ...v }), { success: t("records.removedDone"), invalidate: REF_INVALIDATE, onSuccess: () => setRemoving(null) });
  const restore = useMutate((id: string) => restoreRef({ kind: k, id }), { success: t("records.restored"), invalidate: REF_INVALIDATE });
  useEffect(() => {
    if (!open) setEditing(null);
  }, [open]);
  const items = query.data ?? [];

  return (
    <>
      <Modal
        open={open}
        onClose={onClose}
        size="lg"
        title={t(`catalog.ref.title.${k}`)}
        footer={<Button variant="secondary" onClick={onClose}>{t("common.close")}</Button>}
      >
        <div className="space-y-4">
          {editing ? (
            <RefItemForm kind={k} item={editing === "new" ? null : editing} onDone={() => setEditing(null)} />
          ) : (
            <Button size="sm" icon={<Plus className="size-4" />} onClick={() => setEditing("new")}>{t(`catalog.ref.add.${k}`)}</Button>
          )}
          {query.isError ? (
            <ErrorState error={query.error} retry={() => query.refetch()} />
          ) : query.isLoading ? (
            <div className="py-8"><Spinner className="mx-auto" /></div>
          ) : !items.length ? (
            <p className="py-4 text-center text-sm text-slate-500">{t("catalog.ref.empty")}</p>
          ) : (
            <ul className="divide-y divide-line rounded-xl border border-line">
              {items.map((r) => (
                <li key={r.id} className="flex items-center gap-3 px-3 py-2.5">
                  <div className="min-w-0 flex-1">
                    <p className={r.isActive ? "font-medium text-slate-900" : "font-medium text-slate-400"}>
                      {r.name}
                      {k === "unit" && typeof r.metadata?.symbol === "string" ? <span className="ml-1.5 text-xs text-slate-500">({r.metadata.symbol})</span> : null}
                    </p>
                    <p className="font-mono text-[11px] text-slate-400">{r.code}</p>
                  </div>
                  {!r.isActive ? <Badge tone="slate">{t("catalog.inactive")}</Badge> : null}
                  <Button size="icon" variant="ghost" aria-label={t("catalog.ref.rename", { name: r.name })} title={t("catalog.ref.renameShort")} onClick={() => setEditing(r)}><Pencil className="size-4" /></Button>
                  <Button size="icon" variant="ghost" aria-label={`${t("records.remove")} ${r.name}`} title={t("records.remove")} onClick={() => setRemoving(r)} className="text-slate-500 hover:bg-red-50 hover:text-red-600"><Trash2 className="size-4" /></Button>
                </li>
              ))}
            </ul>
          )}
          <RemovedList items={removed.data} onRestore={(r) => restore.mutate(r.id)} restoringId={restore.isPending ? restore.variables : undefined} className="mt-0" />
        </div>
      </Modal>
      <RemoveDialog
        open={!!removing}
        onClose={() => setRemoving(null)}
        onConfirm={(reason) => removing && remove.mutate({ id: removing.id, reason })}
        title={t("records.removeTitle", { name: removing?.name ?? "" })}
        message={t("catalog.ref.removeMsg")}
        loading={remove.isPending}
        error={remove.error}
        onlineOnly
      />
    </>
  );
}

function RefItemForm({ kind, item, onDone }: { kind: CatalogRefKind; item: RefItem | null; onDone: () => void }) {
  const { t } = useT();
  const [form, setForm] = useState<RefForm>(() => ({
    code: item?.code ?? "",
    codeEdited: !!item,
    names: item ? { ...item.names } : { en: "", am: "", rw: "", fr: "" },
    symbol: typeof item?.metadata?.symbol === "string" ? item.metadata.symbol : "",
  }));
  const [touched, setTouched] = useState(false);
  const save = useMutate(
    () => {
      const names = Object.fromEntries(LANGS.map((l) => [l, form.names[l].trim()])) as Record<Lang, string>;
      const metadata = kind === "unit" ? { ...(item?.metadata ?? {}), symbol: form.symbol.trim() || undefined } : item?.metadata;
      return item
        ? updateRef({ kind, id: item.id, body: { names, ...(metadata ? { metadata } : {}) } })
        : createRef({ kind, body: { code: form.code.trim().toUpperCase(), names, isActive: true, ...(metadata ? { metadata } : {}) } });
    },
    { success: t("refdata.saved"), invalidate: REF_INVALIDATE, onSuccess: () => onDone() },
  );
  const errors: Record<string, string> = {};
  if (!form.names.en.trim()) errors["names.en"] = t("common.required");
  if (!item && !CODE_RE.test(form.code.trim().toUpperCase())) errors.code = form.code.trim() ? t("refdata.codeFormat") : t("common.required");
  const err = (k: string) => (touched ? errors[k] : undefined) ?? fieldError(save.error, k);
  const general = save.error && !["code", "names.en"].some((k) => fieldError(save.error, k)) ? save.error.message : null;

  const setName = (l: Lang, v: string) =>
    setForm((f) => ({ ...f, names: { ...f.names, [l]: v }, code: l === "en" && !f.codeEdited ? codeFromName(v) : f.code }));

  function submit(e: FormEvent) {
    e.preventDefault();
    setTouched(true);
    if (Object.keys(errors).length) return;
    save.mutate(undefined);
  }

  return (
    <form onSubmit={submit} noValidate className="space-y-3 rounded-xl border border-navy-100 bg-navy-50/40 p-4">
      {general ? <Alert tone="red">{general}</Alert> : null}
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label={t("catalog.ref.name")} required error={err("names.en")} htmlFor="ref-en">
          <Input id="ref-en" lang="en" value={form.names.en} onChange={(e) => setName("en", e.target.value)} invalid={!!err("names.en")} maxLength={120} autoComplete="off" autoFocus />
        </Field>
        {item ? null : (
          <Field label={t("refdata.field.code")} required error={err("code")} hint={t("catalog.ref.codeHint")} htmlFor="ref-code">
            <Input id="ref-code" value={form.code} onChange={(e) => setForm((f) => ({ ...f, code: e.target.value.toUpperCase(), codeEdited: true }))} invalid={!!err("code")} className="font-mono uppercase" autoComplete="off" />
          </Field>
        )}
        {kind === "unit" ? (
          <Field label={t("catalog.ref.symbol")} htmlFor="ref-symbol">
            <Input id="ref-symbol" value={form.symbol} onChange={(e) => setForm((f) => ({ ...f, symbol: e.target.value }))} placeholder="kg" maxLength={12} autoComplete="off" />
          </Field>
        ) : null}
      </div>
      <details className="text-sm" open={!!item && LANGS.some((l) => l !== "en" && item.names[l])}>
        <summary className="cursor-pointer font-medium text-slate-600">{t("catalog.ref.otherLanguages")}</summary>
        <div className="mt-3 grid gap-3 sm:grid-cols-3">
          {LANGS.filter((l) => l !== "en").map((l) => (
            <Field key={l} label={l.toUpperCase()} htmlFor={`ref-${l}`}>
              <Input id={`ref-${l}`} lang={l} value={form.names[l]} onChange={(e) => setName(l, e.target.value)} maxLength={120} autoComplete="off" />
            </Field>
          ))}
        </div>
      </details>
      <div className="flex justify-end gap-2">
        <Button size="sm" variant="secondary" onClick={onDone}>{t("common.cancel")}</Button>
        <Button size="sm" type="submit" loading={save.isPending}>{item ? t("common.save") : t("catalog.ref.addShort")}</Button>
      </div>
    </form>
  );
}
