import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { Check, Plus, Trash2, Users } from "lucide-react";
import { Alert, Badge, Button, Checkbox, Field, Input, Modal, Select, StatusBadge, Textarea, type Tone } from "@/components/ui";
import { useQuery } from "@tanstack/react-query";
import { patch, post } from "@/lib/api";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { cn } from "@/lib/cn";
import { formatInt, formatRWF } from "@/lib/format";
import { useT } from "@/i18n";
import { LIFECYCLE_PATH, useCustomerProfile, useSalesMeta, type Contact, type CustomerProfile, type CustomerStatus, type DeliveryProgress, type DuplicateCandidate, type PaymentStatus, type SaleStatus } from "./api";

// ───────────────────────────── order lifecycle (Figure 3) ─────────────────────────────

const ORDER_TONE: Record<SaleStatus, Tone> = {
  DRAFT: "slate", QUOTED: "purple", CONFIRMED: "navy", IN_PRODUCTION: "amber", FROM_STOCK: "blue", READY: "blue",
  DISPATCHED: "amber", DELIVERED: "green", CLOSED: "green", CANCELLED: "red",
};

export function OrderStatusBadge({ status, className }: { status: SaleStatus; className?: string }) {
  const { t } = useT();
  return <Badge tone={ORDER_TONE[status] ?? "slate"} dot className={className}>{t(`orders.status.${status}`)}</Badge>;
}

/** The Figure 3 path with the current step highlighted (cancelled orders show where they stopped). */
export function LifecycleStepper({ status, history }: { status: SaleStatus; history: { to: SaleStatus }[] }) {
  const { t } = useT();
  const reached = new Set(history.map((h) => h.to));
  const current = LIFECYCLE_PATH.findIndex((step) => step.includes(status));
  return (
    <ol className="flex flex-wrap items-center gap-x-1 gap-y-2" aria-label={t("orders.lifecycle.aria")}>
      {LIFECYCLE_PATH.map((step, i) => {
        const done = status !== "CANCELLED" ? i < current : step.some((s) => reached.has(s));
        const here = i === current;
        const taken = step.includes(status) ? status : [...history].reverse().find((h) => step.includes(h.to))?.to;
        const label = step.length > 1 ? (taken ? t(`orders.status.${taken}`) : t("orders.lifecycle.fulfilment")) : t(`orders.status.${step[0]}`);
        return (
          <li key={i} className="flex items-center gap-1" aria-current={here ? "step" : undefined}>
            <span className={cn("inline-flex items-center gap-1 rounded-full px-2.5 py-1 text-xs font-semibold ring-1 ring-inset",
              here ? "bg-navy-700 text-white ring-navy-700" : done ? "bg-emerald-50 text-emerald-700 ring-emerald-200" : "bg-white text-slate-500 ring-slate-200")}>
              {done ? <Check className="size-3" aria-hidden /> : null}
              {label}
            </span>
            {i < LIFECYCLE_PATH.length - 1 ? <span className="h-px w-3 bg-slate-300" aria-hidden /> : null}
          </li>
        );
      })}
      {status === "CANCELLED" ? <li><OrderStatusBadge status="CANCELLED" /></li> : null}
    </ol>
  );
}

export function TransportBadge({ mode }: { mode: "CLIENT" | "BTN" }) {
  const { t } = useT();
  return <Badge tone={mode === "BTN" ? "navy" : "slate"}>{t(`orders.transport.${mode}`)}</Badge>;
}

// ───────────────────────────── status badges ─────────────────────────────

const DELIVERY_PROGRESS: Record<DeliveryProgress, { tone: Tone }> = {
  NOT_DELIVERED: { tone: "slate" },
  PARTIAL: { tone: "blue" },
  DELIVERED: { tone: "green" },
};

/** Delivery progress of a sale (the shared StatusBadge maps PARTIAL to "Part paid"). */
export function DeliveryProgressBadge({ status }: { status: DeliveryProgress }) {
  const { tx } = useT();
  const s = DELIVERY_PROGRESS[status];
  return <Badge tone={s.tone} dot>{tx(`sales.deliveryProgress.${status}`)}</Badge>;
}

export function PaymentBadge({ status }: { status: PaymentStatus }) {
  return <StatusBadge status={status} />;
}

// ───────────────────────────── small presentational helpers ─────────────────────────────

export function Amount({ value, strong, muted, className }: { value: number; strong?: boolean; muted?: boolean; className?: string }) {
  return <span className={cn("tabular whitespace-nowrap", strong && "font-semibold text-slate-900", muted && "text-slate-400", className)}>{formatInt(value)}</span>;
}

export function Rwf({ value, className }: { value: number; className?: string }) {
  return <span className={cn("tabular whitespace-nowrap", className)}>{formatRWF(value)}</span>;
}

/** Label/value pair for detail headers. */
export function Detail({ label, children, className }: { label: string; children: ReactNode; className?: string }) {
  return (
    <div className={cn("min-w-0", className)}>
      <dt className="text-xs font-medium text-slate-500">{label}</dt>
      <dd className="mt-0.5 truncate text-sm font-medium text-slate-800">{children}</dd>
    </div>
  );
}

/** A footer cell row for DataTable totals. */
export function TotalsRow({ cells }: { cells: { content?: ReactNode; align?: "left" | "right"; hideOnMobile?: boolean; colSpan?: number }[] }) {
  return (
    <tr>
      {cells.map((c, i) => (
        <td key={i} colSpan={c.colSpan} className={cn("px-4 py-3", c.align === "right" ? "tabular text-right" : "text-left", c.hideOnMobile && "hidden md:table-cell")}>
          {c.content}
        </td>
      ))}
    </tr>
  );
}

/** Text input that only commits to the URL after the user stops typing. */
export function SearchInput({ value, onChange, placeholder, className }: { value: string; onChange: (v: string) => void; placeholder: string; className?: string }) {
  const [text, setText] = useState(value);
  useEffect(() => setText(value), [value]);
  useEffect(() => {
    if (text === value) return;
    const t = setTimeout(() => onChange(text.trim()), 350);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [text]);
  return <Input type="search" value={text} onChange={(e) => setText(e.target.value)} placeholder={placeholder} className={cn("h-10 w-full sm:w-64", className)} aria-label={placeholder} />;
}

// ───────────────────────────── modals ─────────────────────────────

/** Asks for a mandatory reason (cancel sale, cancel delivery note). */
export function ReasonModal({ open, onClose, title, description, confirmLabel, onSubmit, loading, error }: {
  open: boolean;
  onClose: () => void;
  title: string;
  description?: ReactNode;
  confirmLabel: string;
  onSubmit: (reason: string) => void;
  loading?: boolean;
  error?: unknown;
}) {
  const { t } = useT();
  const [reason, setReason] = useState("");
  useEffect(() => {
    if (open) setReason("");
  }, [open]);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    onSubmit(reason.trim());
  };
  return (
    <Modal
      open={open}
      onClose={onClose}
      title={title}
      size="sm"
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>{t("common.back")}</Button>
          <Button variant="danger" type="submit" form="reason-form" loading={loading} disabled={!reason.trim()}>{confirmLabel}</Button>
        </>
      }
    >
      <form id="reason-form" onSubmit={submit} className="space-y-4">
        {description ? <div className="text-sm text-slate-600">{description}</div> : null}
        <Field label={t("sales.reason.label")} required error={fieldError(error, "reason")} hint={t("sales.reason.hint")}>
          <Textarea value={reason} onChange={(e) => setReason(e.target.value)} maxLength={500} autoFocus rows={3} placeholder={t("sales.reason.placeholder")} />
        </Field>
      </form>
    </Modal>
  );
}

// ───────────────────────────── client (ORD-01 / ORD-02) ─────────────────────────────

interface ClientForm {
  name: string;
  clientType: string;
  tin: string;
  phone: string;
  email: string;
  address: string;
  notes: string;
  paymentTerm: string;
  status: CustomerStatus;
  blockedReason: string;
  contacts: Contact[];
}

const emptyClient: ClientForm = { name: "", clientType: "", tin: "", phone: "", email: "", address: "", notes: "", paymentTerm: "", status: "ACTIVE", blockedReason: "", contacts: [] };
const nul = (s: string) => (s.trim() ? s.trim() : null);

/** Create / edit a client with contacts. Likely duplicates (ORD-02) are shown with the option to save anyway. */
export function CustomerModal({ open, onClose, customerId, onSaved }: { open: boolean; onClose: () => void; customerId?: string | null; onSaved?: (c: { id: string; name: string }) => void }) {
  const { t } = useT();
  const { data: meta } = useSalesMeta();
  const prof = useCustomerProfile(open && customerId ? customerId : undefined);
  const profile: CustomerProfile | null = customerId ? prof.data ?? null : null;
  const [form, setForm] = useState<ClientForm>(emptyClient);
  const [dupes, setDupes] = useState<DuplicateCandidate[] | null>(null);
  useEffect(() => {
    if (!open) return;
    setDupes(null);
    const c = profile?.customer;
    setForm(c ? {
      name: c.name, clientType: c.clientType ?? "", tin: c.tin ?? "", phone: c.phone ?? "", email: c.email ?? "", address: c.address ?? "", notes: c.notes ?? "",
      paymentTerm: c.paymentTerm ?? "", status: c.status, blockedReason: profile?.blockedReason ?? "", contacts: profile?.contacts.map((x) => ({ ...x })) ?? [],
    } : emptyClient);
  }, [open, profile]);
  const id = profile?.customer.id;
  // ORD-02: live duplicate check (debounced) on the identity fields; saving with matches needs "save anyway"
  const probe = JSON.stringify({ name: form.name.trim(), tin: form.tin.trim(), phone: form.phone.trim(), contacts: form.contacts.map((c) => ({ name: c.name || "-", phone: c.phone || null })) });
  const [debounced, setDebounced] = useState(probe);
  useEffect(() => {
    const h = setTimeout(() => setDebounced(probe), 400);
    return () => clearTimeout(h);
  }, [probe]);
  const dupCheck = useQuery({
    queryKey: ["sales", "dupcheck", debounced, id ?? ""],
    queryFn: ({ signal }) => post<{ candidates: DuplicateCandidate[] }>("/sales/customers/duplicates", { ...JSON.parse(debounced), excludeId: id ?? "" }).then((r) => (signal.aborted ? [] : r.candidates)),
    enabled: open && form.name.trim().length >= 3,
    staleTime: 30_000,
  });
  const live = dupCheck.data ?? [];
  const save = useMutate(
    (ack: boolean) => {
      const body = {
        name: form.name, clientType: nul(form.clientType), tin: nul(form.tin), phone: nul(form.phone), email: nul(form.email), address: nul(form.address),
        notes: nul(form.notes), paymentTerm: nul(form.paymentTerm), status: form.status, blockedReason: form.status === "BLOCKED" ? nul(form.blockedReason) : null,
        contacts: form.contacts.filter((c) => c.name.trim()).map((c) => ({ name: c.name.trim(), role: c.role?.trim() || null, phone: c.phone?.trim() || null, email: c.email?.trim() || null, isPrimary: c.isPrimary })),
        acknowledgeDuplicates: ack,
      };
      return id ? patch<CustomerProfile>(`/sales/customers/${id}`, body) : post<CustomerProfile>("/sales/customers", body);
    },
    {
      success: id ? t("sales.customer.updated") : t("sales.customer.added"),
      invalidate: [["sales"], ["lookups"]],
      onSuccess: (p) => {
        onSaved?.({ id: p.customer.id, name: p.customer.name });
        onClose();
      },
    },
  );
  const submitForm = () => {
    if (live.length && !dupes) {
      setDupes(live);
      return;
    }
    save.mutate(!!dupes?.length);
  };
  const set = (k: keyof ClientForm) => (e: { target: { value: string } }) => {
    setDupes(null);
    setForm((f) => ({ ...f, [k]: e.target.value }));
  };
  const err = (k: string) => fieldError(save.error, k);
  const setContact = (i: number, patchC: Partial<Contact>) => setForm((f) => ({ ...f, contacts: f.contacts.map((c, j) => (j === i ? { ...c, ...patchC } : patchC.isPrimary ? { ...c, isPrimary: false } : c)) }));
  return (
    <Modal
      open={open}
      onClose={onClose}
      size="lg"
      title={id ? t("sales.customer.edit") : t("sales.customer.new")}
      description={t("orders.client.hint")}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
          {dupes?.length ? <Button variant="accent" loading={save.isPending} onClick={() => save.mutate(true)}>{t("orders.client.saveAnyway")}</Button> : null}
          <Button type="submit" form="customer-form" loading={save.isPending && !dupes?.length}>{id ? t("common.saveChanges") : t("sales.customer.add")}</Button>
        </>
      }
    >
      <form id="customer-form" className="space-y-5" onSubmit={(e: FormEvent) => { e.preventDefault(); submitForm(); }}>
        {!dupes?.length && live.length ? <Alert tone="blue">{t("orders.client.dupLive", { count: live.length, names: live.map((d) => d.name).join(", ") })}</Alert> : null}
        {dupes?.length ? (
          <Alert tone="amber" title={t("orders.client.dupTitle")}>
            <p className="mb-2">{t("orders.client.dupBody")}</p>
            <ul className="space-y-1">
              {dupes.map((d) => (
                <li key={d.id} className="flex flex-wrap items-center gap-2">
                  <a href={`/sales/customers/${d.id}`} target="_blank" rel="noreferrer" className="font-semibold text-navy-700 underline">{d.name}</a>
                  <Badge tone={d.score >= 90 ? "red" : "amber"}>{t("orders.client.score", { score: d.score })}</Badge>
                  <span className="text-xs">{d.match.map((m) => t(`orders.client.match.${m}`)).join(" · ")}</span>
                </li>
              ))}
            </ul>
          </Alert>
        ) : null}
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t("sales.customer.name")} required error={err("name")} className="sm:col-span-2">
            <Input value={form.name} onChange={set("name")} autoFocus maxLength={160} placeholder={t("sales.customer.namePh")} invalid={!!err("name")} />
          </Field>
          <Field label={t("orders.client.type")} error={err("clientType")}>
            <Select value={form.clientType} onChange={set("clientType")}>
              <option value="">{t("orders.client.typeNone")}</option>
              {meta?.clientTypes.map((c) => <option key={c.code} value={c.code}>{c.name}</option>)}
            </Select>
          </Field>
          <Field label={t("orders.client.status")} error={err("status")}>
            <Select value={form.status} onChange={set("status")}>
              {(["ACTIVE", "INACTIVE", "BLOCKED"] as const).map((s) => <option key={s} value={s}>{t(`orders.client.st.${s}`)}</option>)}
            </Select>
          </Field>
          {form.status === "BLOCKED" ? (
            <Field label={t("orders.client.blockedReason")} required error={err("blockedReason")} className="sm:col-span-2">
              <Input value={form.blockedReason} onChange={set("blockedReason")} maxLength={300} invalid={!!err("blockedReason")} />
            </Field>
          ) : null}
          <Field label={t("sales.customer.tin")} error={err("tin")}>
            <Input value={form.tin} onChange={set("tin")} maxLength={30} inputMode="numeric" invalid={!!err("tin")} />
          </Field>
          <Field label={t("sales.customer.phone")} error={err("phone")}>
            <Input value={form.phone} onChange={set("phone")} maxLength={40} type="tel" invalid={!!err("phone")} />
          </Field>
          <Field label={t("sales.customer.email")} error={err("email")}>
            <Input value={form.email} onChange={set("email")} maxLength={160} type="email" invalid={!!err("email")} />
          </Field>
          <Field label={t("orders.client.paymentTerm")} error={err("paymentTerm")}>
            <Select value={form.paymentTerm} onChange={set("paymentTerm")}>
              <option value="">{t("orders.client.termNone")}</option>
              {meta?.paymentTerms.map((p) => <option key={p.code} value={p.code}>{p.name}</option>)}
            </Select>
          </Field>
          <Field label={t("sales.customer.address")} error={err("address")} className="sm:col-span-2">
            <Input value={form.address} onChange={set("address")} maxLength={300} placeholder={t("sales.customer.addressPh")} invalid={!!err("address")} />
          </Field>
          <Field label={t("common.notes")} error={err("notes")} className="sm:col-span-2">
            <Textarea value={form.notes} onChange={set("notes")} rows={2} maxLength={2000} />
          </Field>
        </div>
        <fieldset className="space-y-3">
          <legend className="flex w-full items-center justify-between text-sm font-semibold text-slate-800">
            <span className="flex items-center gap-2"><Users className="size-4" aria-hidden /> {t("orders.client.contacts")}</span>
            <Button size="sm" variant="secondary" icon={<Plus className="size-4" />} disabled={form.contacts.length >= 20}
              onClick={() => setForm((f) => ({ ...f, contacts: [...f.contacts, { name: "", role: "", phone: "", email: "", isPrimary: f.contacts.length === 0 }] }))}>{t("orders.client.addContact")}</Button>
          </legend>
          {form.contacts.length === 0 ? <p className="text-xs text-slate-500">{t("orders.client.noContacts")}</p> : null}
          {form.contacts.map((c, i) => (
            <div key={i} className="grid gap-2 rounded-xl border border-line p-3 sm:grid-cols-[1fr_1fr_1fr_1fr_auto]">
              <Field label={t("orders.client.contactName")} error={err(`contacts.${i}.name`)}><Input value={c.name} onChange={(e) => setContact(i, { name: e.target.value })} maxLength={120} /></Field>
              <Field label={t("orders.client.contactRole")}><Input value={c.role ?? ""} onChange={(e) => setContact(i, { role: e.target.value })} maxLength={80} /></Field>
              <Field label={t("sales.customer.phone")} error={err(`contacts.${i}.phone`)}><Input type="tel" value={c.phone ?? ""} onChange={(e) => setContact(i, { phone: e.target.value })} maxLength={40} /></Field>
              <Field label={t("sales.customer.email")} error={err(`contacts.${i}.email`)}><Input type="email" value={c.email ?? ""} onChange={(e) => setContact(i, { email: e.target.value })} maxLength={160} /></Field>
              <div className="flex items-end gap-2 pb-1">
                <Checkbox label={t("orders.client.primary")} checked={c.isPrimary} onChange={(e) => setContact(i, { isPrimary: e.target.checked })} />
                <Button variant="ghost" size="icon" aria-label={t("orders.client.removeContact", { n: i + 1 })} onClick={() => setForm((f) => ({ ...f, contacts: f.contacts.filter((_, j) => j !== i) }))}><Trash2 className="size-4" /></Button>
              </div>
            </div>
          ))}
        </fieldset>
      </form>
    </Modal>
  );
}
