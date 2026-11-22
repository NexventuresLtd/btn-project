import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { Badge, Button, Checkbox, Field, Input, Modal, StatusBadge, Textarea, type Tone } from "@/components/ui";
import { patch, post } from "@/lib/api";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { cn } from "@/lib/cn";
import { formatInt, formatRWF } from "@/lib/format";
import { useT } from "@/i18n";
import type { Customer, DeliveryProgress, PaymentStatus } from "./api";

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

const emptyCustomer = { name: "", tin: "", phone: "", email: "", address: "", isActive: true };

/** Create / edit a customer through the master-data API (permission customers.manage). */
export function CustomerModal({ open, onClose, customer, onSaved }: { open: boolean; onClose: () => void; customer?: Customer | null; onSaved?: (c: Customer) => void }) {
  const { t } = useT();
  const [form, setForm] = useState(emptyCustomer);
  useEffect(() => {
    if (!open) return;
    setForm(
      customer
        ? { name: customer.name, tin: customer.tin ?? "", phone: customer.phone ?? "", email: customer.email ?? "", address: customer.address ?? "", isActive: customer.isActive }
        : emptyCustomer,
    );
  }, [open, customer]);
  const save = useMutate(
    (body: typeof form) => (customer ? patch<Customer>(`/masterdata/customers/${customer.id}`, body) : post<Customer>("/masterdata/customers", body)),
    {
      success: customer ? t("sales.customer.updated") : t("sales.customer.added"),
      invalidate: [["sales"], ["lookups"]],
      onSuccess: (c) => {
        onSaved?.(c);
        onClose();
      },
    },
  );
  const set = (k: keyof typeof form) => (e: { target: { value: string } }) => setForm((f) => ({ ...f, [k]: e.target.value }));
  const err = (k: string) => fieldError(save.error, k);
  return (
    <Modal
      open={open}
      onClose={onClose}
      title={customer ? t("sales.customer.edit") : t("sales.customer.new")}
      description={t("sales.customer.hint")}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
          <Button type="submit" form="customer-form" loading={save.isPending}>{customer ? t("common.saveChanges") : t("sales.customer.add")}</Button>
        </>
      }
    >
      <form
        id="customer-form"
        className="grid gap-4 sm:grid-cols-2"
        onSubmit={(e) => {
          e.preventDefault();
          save.mutate(form);
        }}
      >
        <Field label={t("sales.customer.name")} required error={err("name")} className="sm:col-span-2">
          <Input value={form.name} onChange={set("name")} autoFocus maxLength={160} placeholder={t("sales.customer.namePh")} invalid={!!err("name")} />
        </Field>
        <Field label={t("sales.customer.tin")} error={err("tin")}>
          <Input value={form.tin} onChange={set("tin")} maxLength={30} inputMode="numeric" invalid={!!err("tin")} />
        </Field>
        <Field label={t("sales.customer.phone")} error={err("phone")}>
          <Input value={form.phone} onChange={set("phone")} maxLength={30} type="tel" invalid={!!err("phone")} />
        </Field>
        <Field label={t("sales.customer.email")} error={err("email")} className="sm:col-span-2">
          <Input value={form.email} onChange={set("email")} maxLength={120} type="email" invalid={!!err("email")} />
        </Field>
        <Field label={t("sales.customer.address")} error={err("address")} className="sm:col-span-2">
          <Input value={form.address} onChange={set("address")} maxLength={300} placeholder={t("sales.customer.addressPh")} invalid={!!err("address")} />
        </Field>
        {customer ? (
          <Checkbox label={t("sales.customer.activeHint")} checked={form.isActive} onChange={(e) => setForm((f) => ({ ...f, isActive: e.target.checked }))} className="sm:col-span-2" />
        ) : null}
      </form>
    </Modal>
  );
}
