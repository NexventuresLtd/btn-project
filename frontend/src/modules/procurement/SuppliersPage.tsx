import { useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { Plus, Trash2 } from "lucide-react";
import { Button, Card, DataTable, ErrorState, ExportMenu, Field, Input, Modal, PageHeader, Pagination, Select, Textarea, type Column } from "@/components/ui";
import { Can } from "@/auth/guards";
import { useT } from "@/i18n";
import { useLookups } from "@/hooks/useLookups";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { formatDate } from "@/lib/dates";
import { createSupplier, PROC_KEY, SUPPLIER_STATUSES, updateSupplier, useSuppliers, type Contact, type Supplier, type SupplierInput } from "./api";
import { Money, SearchInput, SupplierStatusBadge } from "./components";

export function SuppliersPage() {
  const { t, tx } = useT();
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const { data: lookups } = useLookups();
  const q = params.get("q") ?? "";
  const status = params.get("status") ?? "";
  const category = params.get("category") ?? "";
  const page = Number(params.get("page") ?? 1);
  const set = (k: string, v: string | null) => {
    const n = new URLSearchParams(params);
    if (v) n.set(k, v); else n.delete(k);
    if (k !== "page") n.delete("page");
    setParams(n, { replace: true });
  };
  const list = useSuppliers({ q: q || undefined, status: status || undefined, category: category || undefined, page });
  const [editing, setEditing] = useState<Supplier | "new" | null>(null);
  const cats = useCategoryOptions();
  void lookups;

  const columns: Column<Supplier>[] = [
    { key: "name", header: t("procurement.col.supplier"), cell: (s) => (
      <div className="min-w-[12rem]"><Link to={`/procurement/suppliers/${s.id}`} onClick={(e) => e.stopPropagation()} className="font-semibold text-navy-800 hover:underline">{s.name}</Link>
        <p className="text-xs text-slate-500">{s.code}{s.tin ? ` · TIN ${s.tin}` : ""}</p></div>
    ) },
    { key: "contact", header: t("procurement.col.contact"), hideOnMobile: true, cell: (s) => <div className="text-xs text-slate-600"><p>{s.contactName ?? "—"}</p><p>{[s.phone, s.email].filter(Boolean).join(" · ")}</p></div> },
    { key: "cats", header: t("procurement.col.categories"), hideOnMobile: true, cell: (s) => <span className="text-xs text-slate-600">{s.categoryNames.join(", ") || "—"}</span> },
    { key: "orders", header: t("procurement.col.orders"), align: "right", cell: (s) => s.stats?.orders ?? 0 },
    { key: "value", header: t("procurement.col.orderedValue"), align: "right", cell: (s) => <Money value={s.stats?.orderedValue} strong /> },
    { key: "last", header: t("procurement.col.lastOrder"), hideOnMobile: true, cell: (s) => (s.stats?.lastOrderDate ? formatDate(s.stats.lastOrderDate) : "—") },
    { key: "status", header: t("common.status"), cell: (s) => <SupplierStatusBadge status={s.status} /> },
  ];

  return (
    <>
      <PageHeader title={t("procurement.suppliers.title")} description={t("procurement.suppliers.subtitle")}
        actions={<><ExportMenu path="/procurement/suppliers" params={{ q, status, category }} /><Can permission="procurement.manage"><Button icon={<Plus className="size-4" />} onClick={() => setEditing("new")}>{t("procurement.suppliers.new")}</Button></Can></>} />
      <Card>
        <div className="flex flex-wrap items-center gap-2 px-5 pt-5 pb-4">
          <SearchInput value={q} onChange={(v) => set("q", v)} placeholder={t("procurement.suppliers.search")} />
          <Select value={status} onChange={(e) => set("status", e.target.value || null)} className="w-full sm:w-44" aria-label={t("common.status")}>
            <option value="">{t("procurement.suppliers.anyStatus")}</option>
            {SUPPLIER_STATUSES.map((s) => <option key={s} value={s}>{tx(`procurement.supplierStatus.${s}`)}</option>)}
          </Select>
          <Select value={category} onChange={(e) => set("category", e.target.value || null)} className="w-full sm:w-56" aria-label={t("procurement.col.categories")}>
            <option value="">{t("procurement.suppliers.anyCategory")}</option>
            {cats.map((c) => <option key={c.value} value={c.value}>{c.label}</option>)}
          </Select>
          {list.data ? <span className="ml-auto text-xs text-slate-500">{t("procurement.list.count", { count: list.data.total })}</span> : null}
        </div>
        {list.isError ? <ErrorState error={list.error} retry={() => list.refetch()} /> : (
          <>
            <DataTable columns={columns} rows={list.data?.items} rowKey={(s) => s.id} loading={list.isLoading} onRowClick={(s) => navigate(`/procurement/suppliers/${s.id}`)}
              empty={<div className="space-y-1"><p className="font-medium text-slate-700">{t("procurement.suppliers.emptyTitle")}</p><p>{t("procurement.suppliers.emptyHint")}</p></div>} />
            {list.data ? <Pagination page={list.data.page} pageSize={list.data.pageSize} total={list.data.total} onPage={(p) => set("page", String(p))} /> : null}
          </>
        )}
      </Card>
      {editing ? <SupplierModal supplier={editing === "new" ? null : editing} onClose={() => setEditing(null)} /> : null}
    </>
  );
}

export function useCategoryOptions() {
  const { t } = useT();
  const { data: lookups } = useLookups();
  const ref = lookups?.refdata ?? {};
  return [
    ...(ref.material_category ?? []).map((c) => ({ value: `material_category:${c.code}`, label: `${t("procurement.itemType.MATERIAL")} · ${c.name}` })),
    ...(ref.product_category ?? []).map((c) => ({ value: `product_category:${c.code}`, label: `${t("procurement.itemType.PRODUCT")} · ${c.name}` })),
    ...(ref.expense_category ?? []).map((c) => ({ value: `expense_category:${c.code}`, label: `${t("procurement.field.expenseCategory")} · ${c.name}` })),
  ];
}

export function SupplierModal({ supplier, onClose }: { supplier: Supplier | null; onClose: () => void }) {
  const { t, tx } = useT();
  const { data: lookups } = useLookups();
  const cats = useCategoryOptions();
  const [f, setF] = useState<SupplierInput>({
    name: supplier?.name ?? "", tin: supplier?.tin ?? "", contactName: supplier?.contactName ?? "", phone: supplier?.phone ?? "", email: supplier?.email ?? "", address: supplier?.address ?? "",
    contacts: supplier?.contacts ?? [], categories: supplier?.categories ?? [], paymentTerm: supplier?.paymentTerm ?? "", status: supplier?.status ?? "ACTIVE", notes: supplier?.notes ?? "",
  });
  const m = useMutate((b: SupplierInput) => (supplier ? updateSupplier(supplier.id, b) : createSupplier(b)), { invalidate: [PROC_KEY], success: t("procurement.suppliers.saved"), onSuccess: onClose });
  const fe = (n: string) => fieldError(m.error, n);
  const s = (k: keyof SupplierInput, v: unknown) => setF((x) => ({ ...x, [k]: v }));
  const contacts = f.contacts ?? [];
  const setContact = (i: number, p: Partial<Contact>) => s("contacts", contacts.map((c, j) => (j === i ? { ...c, ...p } : c)));
  const nul = (v: string | null | undefined) => (v && v.trim() ? v.trim() : null);
  return (
    <Modal open onClose={onClose} size="lg" title={supplier ? t("procurement.suppliers.edit", { name: supplier.name }) : t("procurement.suppliers.new")}
      footer={<><Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
        <Button loading={m.isPending} onClick={() => m.mutate({ ...f, name: f.name.trim(), tin: nul(f.tin), contactName: nul(f.contactName), phone: nul(f.phone), email: nul(f.email), address: nul(f.address), notes: nul(f.notes), paymentTerm: nul(f.paymentTerm),
          contacts: contacts.filter((c) => c.name.trim()) })}>{t("common.save")}</Button></>}>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label={t("procurement.suppliers.name")} required error={fe("name")} htmlFor="s-name"><Input id="s-name" value={f.name} maxLength={150} onChange={(e) => s("name", e.target.value)} invalid={!!fe("name")} autoFocus /></Field>
        <Field label={t("procurement.col.tin")} error={fe("tin")} htmlFor="s-tin"><Input id="s-tin" value={f.tin ?? ""} maxLength={20} onChange={(e) => s("tin", e.target.value)} invalid={!!fe("tin")} /></Field>
        <Field label={t("procurement.col.contact")} error={fe("contactName")} htmlFor="s-c"><Input id="s-c" value={f.contactName ?? ""} maxLength={120} onChange={(e) => s("contactName", e.target.value)} /></Field>
        <Field label={t("procurement.col.phone")} error={fe("phone")} htmlFor="s-p"><Input id="s-p" type="tel" value={f.phone ?? ""} maxLength={20} onChange={(e) => s("phone", e.target.value)} invalid={!!fe("phone")} /></Field>
        <Field label={t("procurement.col.email")} error={fe("email")} htmlFor="s-e"><Input id="s-e" type="email" value={f.email ?? ""} maxLength={120} onChange={(e) => s("email", e.target.value)} invalid={!!fe("email")} /></Field>
        <Field label={t("procurement.field.paymentTerm")} error={fe("paymentTerm")} htmlFor="s-t">
          <Select id="s-t" value={f.paymentTerm ?? ""} onChange={(e) => s("paymentTerm", e.target.value)}>
            <option value="">—</option>
            {(lookups?.refdata?.payment_term ?? []).map((p) => <option key={p.code} value={p.code}>{p.name}</option>)}
          </Select>
        </Field>
        <Field label={t("common.status")} htmlFor="s-st">
          <Select id="s-st" value={f.status} onChange={(e) => s("status", e.target.value)}>
            {SUPPLIER_STATUSES.map((x) => <option key={x} value={x}>{tx(`procurement.supplierStatus.${x}`)}</option>)}
          </Select>
        </Field>
        <Field label={t("procurement.suppliers.address")} error={fe("address")} htmlFor="s-a"><Input id="s-a" value={f.address ?? ""} maxLength={300} onChange={(e) => s("address", e.target.value)} /></Field>
        <fieldset className="sm:col-span-2">
          <legend className="mb-1.5 text-sm font-medium text-slate-700">{t("procurement.col.categories")}</legend>
          <div className="flex flex-wrap gap-2">
            {cats.map((c) => {
              const on = (f.categories ?? []).includes(c.value);
              return (
                <button key={c.value} type="button" aria-pressed={on} onClick={() => s("categories", on ? (f.categories ?? []).filter((x) => x !== c.value) : [...(f.categories ?? []), c.value])}
                  className={on ? "rounded-full bg-navy-800 px-3 py-1 text-xs font-semibold text-white" : "rounded-full border border-line bg-white px-3 py-1 text-xs font-semibold text-slate-600 hover:bg-slate-50"}>{c.label}</button>
              );
            })}
          </div>
          {fe("categories") ? <p className="mt-1 text-xs text-red-600">{fe("categories")}</p> : null}
        </fieldset>
        <fieldset className="sm:col-span-2">
          <legend className="mb-1.5 text-sm font-medium text-slate-700">{t("procurement.suppliers.contacts")}</legend>
          <div className="space-y-2">
            {contacts.map((c, i) => (
              <div key={i} className="grid gap-2 sm:grid-cols-[1fr_1fr_1fr_1fr_auto]">
                <Input value={c.name} placeholder={t("common.name")} aria-label={t("common.name")} onChange={(e) => setContact(i, { name: e.target.value })} invalid={!!fe(`contacts.${i}.name`)} />
                <Input value={c.role} placeholder={t("procurement.suppliers.role")} aria-label={t("procurement.suppliers.role")} onChange={(e) => setContact(i, { role: e.target.value })} />
                <Input value={c.phone} type="tel" placeholder={t("procurement.col.phone")} aria-label={t("procurement.col.phone")} onChange={(e) => setContact(i, { phone: e.target.value })} invalid={!!fe(`contacts.${i}.phone`)} />
                <Input value={c.email} type="email" placeholder={t("procurement.col.email")} aria-label={t("procurement.col.email")} onChange={(e) => setContact(i, { email: e.target.value })} invalid={!!fe(`contacts.${i}.email`)} />
                <Button variant="ghost" size="icon" aria-label={t("common.remove")} onClick={() => s("contacts", contacts.filter((_, j) => j !== i))}><Trash2 className="size-4" /></Button>
              </div>
            ))}
            <Button size="sm" variant="secondary" icon={<Plus className="size-3.5" />} disabled={contacts.length >= 10} onClick={() => s("contacts", [...contacts, { name: "", role: "", phone: "", email: "" }])}>{t("procurement.suppliers.addContact")}</Button>
          </div>
        </fieldset>
        <Field label={t("procurement.field.notes")} className="sm:col-span-2" htmlFor="s-n"><Textarea id="s-n" value={f.notes ?? ""} maxLength={1000} onChange={(e) => s("notes", e.target.value)} /></Field>
      </div>
    </Modal>
  );
}
