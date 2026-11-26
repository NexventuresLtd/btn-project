import { useEffect, useState, type FormEvent } from "react";
import { ArrowDown, ArrowUp, ListTree, Pencil, Plus, Power, Scale, Trash2 } from "lucide-react";
import { LANG_NAMES, useT, type Lang } from "@/i18n";
import { useAuth } from "@/auth/AuthProvider";
import {
  Alert, Badge, Button, Card, CardHeader, Checkbox, DataTable, EmptyState, ErrorState, Field, Input, Modal, PageHeader, RemoveDialog, Select,
  Textarea, type Column,
} from "@/components/ui";
import { fieldError, useMutate } from "@/hooks/useMutate";
import type { RefItem } from "@/hooks/useLookups";
import { formatRWF } from "@/lib/format";
import { formatDateTime } from "@/lib/dates";
import { cn } from "@/lib/cn";
import {
  APPROVAL_DOMAINS, REF_KINDS, REFDATA_KEY, createRef, createThreshold, deleteThreshold, fetchLevels, reorderRef, updateRef, updateThreshold,
  useRefData, useThresholds, type ApprovalDomain, type ApprovalLevel, type RefKind, type Threshold,
} from "./refdataApi";

type Tab = RefKind | "thresholds";
const LANGS_ORDER: Lang[] = ["en", "am", "rw", "fr"];
const CODE_RE = /^[A-Z0-9][A-Z0-9_-]{0,39}$/;
const INVALIDATE = [[...REFDATA_KEY], ["lookups"]];

/** Administration → Reference data (ADM-01): one tab per list kind + approval thresholds. */
export function RefDataPage() {
  const { t } = useT();
  const { can } = useAuth();
  const [tab, setTab] = useState<Tab>(() => {
    const h = typeof location !== "undefined" ? location.hash.slice(1) : "";
    return (REF_KINDS as readonly string[]).includes(h) || h === "thresholds" ? (h as Tab) : "defect_reason";
  });
  useEffect(() => {
    history.replaceState(null, "", `#${tab}`);
  }, [tab]);
  const tabs: { value: Tab; label: string }[] = [
    ...REF_KINDS.map((k) => ({ value: k as Tab, label: t(`refdata.kind.${k}`) })),
    ...(can("settings.manage") ? [{ value: "thresholds" as Tab, label: t("refdata.thresholds") }] : []),
  ];

  return (
    <>
      <PageHeader title={t("refdata.title")} description={t("refdata.description")} breadcrumbs={[{ label: t("refdata.breadcrumb") }, { label: t("refdata.title") }]} />
      <div className="mb-5">
        <div className="flex flex-wrap gap-1 rounded-xl bg-slate-100 p-1" role="tablist" aria-label={t("refdata.title")}>
          {tabs.map((x) => (
            <button
              key={x.value}
              role="tab"
              id={`tab-${x.value}`}
              aria-selected={tab === x.value}
              aria-controls="refdata-panel"
              onClick={() => setTab(x.value)}
              className={cn(
                "rounded-lg px-3 py-1.5 text-[13px] font-semibold whitespace-nowrap transition focus-visible:ring-2 focus-visible:ring-navy-300 focus-visible:outline-none",
                tab === x.value ? "bg-white text-navy-800 shadow-sm" : "text-slate-600 hover:text-slate-900",
              )}
            >
              {x.value === "thresholds" ? <Scale className="mr-1 inline size-3.5" aria-hidden="true" /> : null}
              {x.label}
            </button>
          ))}
        </div>
      </div>
      <div id="refdata-panel" role="tabpanel" aria-labelledby={`tab-${tab}`}>
        {tab === "thresholds" ? <ThresholdsPanel /> : <KindPanel key={tab} kind={tab} />}
      </div>
    </>
  );
}

// ───────────────────────────── one list kind ─────────────────────────────

function KindPanel({ kind }: { kind: RefKind }) {
  const { t, lang } = useT();
  const [showInactive, setShowInactive] = useState(false);
  const query = useRefData(kind, true);
  const [editing, setEditing] = useState<RefItem | "new" | null>(null);
  const all = query.data ?? [];
  const rows = showInactive ? all : all.filter((r) => r.isActive);

  const toggle = useMutate((r: RefItem) => updateRef({ kind, id: r.id, body: { isActive: !r.isActive } }), { success: t("refdata.saved"), invalidate: INVALIDATE });
  const reorder = useMutate(reorderRef, { success: t("refdata.reordered"), invalidate: INVALIDATE });

  function move(item: RefItem, dir: -1 | 1) {
    const ids = all.map((r) => r.id);
    const i = ids.indexOf(item.id);
    const j = i + dir;
    if (j < 0 || j >= ids.length) return;
    [ids[i], ids[j]] = [ids[j]!, ids[i]!];
    reorder.mutate({ kind, ids });
  }

  const columns: Column<RefItem>[] = [
    {
      key: "order",
      header: t("refdata.col.order"),
      className: "w-24",
      cell: (r) => {
        const i = all.indexOf(r);
        return (
          <div className="flex gap-1">
            <Button size="sm" variant="ghost" aria-label={t("refdata.moveUp", { name: r.name })} disabled={i <= 0 || reorder.isPending} onClick={() => move(r, -1)} className="px-1.5"><ArrowUp className="size-3.5" /></Button>
            <Button size="sm" variant="ghost" aria-label={t("refdata.moveDown", { name: r.name })} disabled={i >= all.length - 1 || reorder.isPending} onClick={() => move(r, 1)} className="px-1.5"><ArrowDown className="size-3.5" /></Button>
          </div>
        );
      },
    },
    { key: "code", header: t("refdata.col.code"), cell: (r) => <span className="font-mono text-xs font-semibold text-slate-700">{r.code}</span> },
    { key: "name", header: t("refdata.col.name"), cell: (r) => <span className="font-medium text-slate-900">{r.name}</span> },
    {
      key: "tr",
      header: t("refdata.col.translations"),
      hideOnMobile: true,
      cell: (r) => (
        <div className="space-y-0.5 text-xs text-slate-500">
          {LANGS_ORDER.filter((l) => l !== lang).map((l) => (
            <p key={l} lang={l}><span className="mr-1 font-semibold uppercase text-slate-400">{l}</span>{r.names[l] || <span className="italic text-amber-700">—</span>}</p>
          ))}
        </div>
      ),
    },
    { key: "status", header: t("refdata.col.status"), cell: (r) => <Badge tone={r.isActive ? "green" : "slate"} dot>{r.isActive ? t("refdata.active") : t("refdata.inactive")}</Badge> },
    {
      key: "actions",
      header: <span className="sr-only">{t("refdata.col.actions")}</span>,
      align: "right",
      cell: (r) => (
        <div className="flex justify-end gap-1">
          <Button size="sm" variant="secondary" icon={<Pencil className="size-3.5" />} onClick={() => setEditing(r)}>{t("common.edit")}</Button>
          <Button size="sm" variant="ghost" icon={<Power className="size-3.5" />} loading={toggle.isPending && toggle.variables?.id === r.id} onClick={() => toggle.mutate(r)} className={r.isActive ? "text-red-600 hover:bg-red-50" : "text-emerald-700 hover:bg-emerald-50"}>
            {r.isActive ? t("refdata.deactivate") : t("refdata.activate")}
          </Button>
        </div>
      ),
    },
  ];

  return (
    <Card>
      <CardHeader
        title={t(`refdata.kind.${kind}`)}
        actions={
          <div className="flex flex-wrap items-center gap-3">
            <Checkbox label={t("refdata.showInactive")} checked={showInactive} onChange={(e) => setShowInactive(e.target.checked)} />
            <Button icon={<Plus className="size-4" />} onClick={() => setEditing("new")}>{t("refdata.new")}</Button>
          </div>
        }
      />
      {query.isError ? (
        <ErrorState error={query.error} retry={() => query.refetch()} />
      ) : (
        <DataTable
          columns={columns}
          rows={rows}
          rowKey={(r) => r.id}
          loading={query.isLoading}
          empty={<EmptyState icon={<ListTree />} title={t("refdata.empty")} description={t("refdata.emptyText")} action={<Button onClick={() => setEditing("new")}>{t("refdata.new")}</Button>} />}
        />
      )}
      <ItemModal kind={kind} item={editing === "new" ? null : editing} open={editing !== null} onClose={() => setEditing(null)} />
    </Card>
  );
}

interface ItemForm { code: string; names: Record<Lang, string>; metadata: string; isActive: boolean }

function ItemModal({ kind, item, open, onClose }: { kind: RefKind; item: RefItem | null; open: boolean; onClose: () => void }) {
  const { t } = useT();
  const blank: ItemForm = { code: "", names: { en: "", am: "", rw: "", fr: "" }, metadata: "", isActive: true };
  const [form, setForm] = useState<ItemForm>(blank);
  const [touched, setTouched] = useState(false);
  useEffect(() => {
    if (!open) return;
    setTouched(false);
    setForm(item ? { code: item.code, names: { ...item.names }, metadata: Object.keys(item.metadata ?? {}).length ? JSON.stringify(item.metadata, null, 2) : "", isActive: item.isActive } : blank);
  }, [open, item]); // eslint-disable-line react-hooks/exhaustive-deps

  const save = useMutate(
    (body: { code?: string; names: Record<Lang, string>; metadata: Record<string, unknown>; isActive: boolean }) =>
      item ? updateRef({ kind, id: item.id, body: { names: body.names, metadata: body.metadata, isActive: body.isActive } }) : createRef({ kind, body }),
    { success: t("refdata.saved"), invalidate: INVALIDATE, onSuccess: () => onClose() },
  );

  let meta: Record<string, unknown> | null = {};
  if (form.metadata.trim()) {
    try {
      const v: unknown = JSON.parse(form.metadata);
      meta = v && typeof v === "object" && !Array.isArray(v) ? (v as Record<string, unknown>) : null;
    } catch {
      meta = null;
    }
  }
  const errors: Record<string, string> = {};
  if (!item && !CODE_RE.test(form.code.trim().toUpperCase())) errors.code = form.code.trim() ? t("refdata.codeFormat") : t("refdata.required");
  if (!form.names.en.trim()) errors["names.en"] = t("refdata.required");
  if (meta === null) errors.metadata = t("refdata.field.metadataInvalid");
  const err = (k: string) => (touched ? errors[k] : undefined) ?? fieldError(save.error, k);

  function submit(e: FormEvent) {
    e.preventDefault();
    setTouched(true);
    if (Object.keys(errors).length || meta === null) return;
    const names = Object.fromEntries(LANGS_ORDER.map((l) => [l, form.names[l].trim()])) as Record<Lang, string>;
    save.mutate({ ...(item ? {} : { code: form.code.trim().toUpperCase() }), names, metadata: meta, isActive: form.isActive });
  }

  const general = save.error && !["code", "names.en", "metadata"].some((k) => fieldError(save.error, k)) ? save.error.message : null;
  return (
    <Modal
      open={open}
      onClose={onClose}
      title={item ? t("refdata.edit") : t("refdata.new")}
      description={t(`refdata.kind.${kind}`)}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
          <Button type="submit" form="ref-form" loading={save.isPending}>{item ? t("common.saveChanges") : t("refdata.create")}</Button>
        </>
      }
    >
      <form id="ref-form" onSubmit={submit} noValidate className="grid gap-4 sm:grid-cols-2">
        {general ? <div className="sm:col-span-2"><Alert tone="red">{general}</Alert></div> : null}
        <Field label={t("refdata.field.code")} required={!item} error={err("code")} hint={t("refdata.field.codeHint")} htmlFor="ref-code" className="sm:col-span-2">
          <Input id="ref-code" value={form.code} disabled={!!item} onChange={(e) => setForm((f) => ({ ...f, code: e.target.value.toUpperCase() }))} invalid={!!err("code")} className="font-mono uppercase" autoComplete="off" autoFocus={!item} />
        </Field>
        {LANGS_ORDER.map((l) => (
          <Field key={l} label={t("refdata.field.nameIn", { language: LANG_NAMES[l] })} required={l === "en"} error={err(`names.${l}`)} htmlFor={`ref-name-${l}`} hint={l === "am" ? t("refdata.field.nameHint") : undefined}>
            <Input id={`ref-name-${l}`} lang={l} value={form.names[l]} onChange={(e) => setForm((f) => ({ ...f, names: { ...f.names, [l]: e.target.value } }))} invalid={!!err(`names.${l}`)} maxLength={120} autoComplete="off" autoFocus={!!item && l === "en"} />
          </Field>
        ))}
        <Field label={t("refdata.field.metadata")} error={err("metadata")} hint={t("refdata.field.metadataHint")} htmlFor="ref-meta" className="sm:col-span-2">
          <Textarea id="ref-meta" value={form.metadata} onChange={(e) => setForm((f) => ({ ...f, metadata: e.target.value }))} rows={3} className="font-mono text-xs" invalid={!!err("metadata")} spellCheck={false} />
        </Field>
        <div className="sm:col-span-2">
          <Checkbox label={t("refdata.field.activeLabel")} checked={form.isActive} onChange={(e) => setForm((f) => ({ ...f, isActive: e.target.checked }))} />
        </div>
      </form>
    </Modal>
  );
}

// ───────────────────────────── approval thresholds ─────────────────────────────

const APPROVAL_PERMISSIONS = ["sales.approve", "procurement.approve", "expenses.approve", "pettycash.approve", "transfers.approve", "stock.adjust", "stockcount.approve", "budgets.approve"];

function ThresholdsPanel() {
  const { t, tx } = useT();
  const [showRetired, setShowRetired] = useState(false);
  const query = useThresholds(showRetired);
  const [domain, setDomain] = useState<ApprovalDomain | "">("");
  const [editing, setEditing] = useState<Threshold | "new" | null>(null);
  const [deleting, setDeleting] = useState<Threshold | null>(null);
  const remove = useMutate(deleteThreshold, { success: t("refdata.th.deleted"), invalidate: INVALIDATE, onSuccess: () => setDeleting(null) });
  const rows = (query.data ?? []).filter((r) => !domain || r.domain === domain);
  const range = (r: { minAmount: number; maxAmount: number | null }) =>
    r.maxAmount == null ? t("refdata.th.fromAmount", { amount: formatRWF(r.minAmount) }) : t("refdata.th.rangeAmount", { min: formatRWF(r.minAmount), max: formatRWF(r.maxAmount) });

  const columns: Column<Threshold>[] = [
    { key: "domain", header: t("refdata.th.col.domain"), cell: (r) => <span className="font-medium text-slate-900">{t(`refdata.domain.${r.domain}`)}</span> },
    { key: "level", header: t("refdata.th.col.level"), align: "center", cell: (r) => <Badge tone="navy">L{r.level}</Badge> },
    { key: "range", header: t("refdata.th.col.range"), cell: (r) => <span className="tabular-nums">{range(r)}</span> },
    {
      key: "approver",
      header: t("refdata.th.col.approver"),
      cell: (r) => (r.role ? tx(`enums.role.${r.role}`, undefined, r.role) : t("refdata.th.anyRole", { permission: r.permission })),
    },
    { key: "label", header: t("refdata.th.col.label"), hideOnMobile: true, cell: (r) => (r.retiredAt ? null :
      <span className="text-slate-600">{r.label || "—"}{r.retiredAt ? <span className="block text-xs text-slate-500">{t("records.removedOn", { date: formatDateTime(r.retiredAt) })}{r.retiredReason ? ` — ${r.retiredReason}` : ""}</span> : null}</span>
    ) },
    { key: "status", header: t("refdata.col.status"), cell: (r) => (r.retiredAt ? <Badge tone="slate">{t("records.removed")}</Badge> : <Badge tone={r.isActive ? "green" : "slate"} dot>{r.isActive ? t("refdata.active") : t("refdata.inactive")}</Badge>) },
    {
      key: "actions",
      header: <span className="sr-only">{t("refdata.col.actions")}</span>,
      align: "right",
      cell: (r) => (r.retiredAt ? null :
        <div className="flex justify-end gap-1">
          <Button size="sm" variant="secondary" icon={<Pencil className="size-3.5" />} onClick={() => setEditing(r)}>{t("common.edit")}</Button>
          <Button size="sm" variant="ghost" icon={<Trash2 className="size-3.5" />} aria-label={t("refdata.th.delete")} onClick={() => setDeleting(r)} className="text-red-600 hover:bg-red-50" />
        </div>
      ),
    },
  ];

  return (
    <div className="space-y-4">
      <Alert tone="blue">{t("refdata.th.description")}</Alert>
      <Card>
        <CardHeader
          title={t("refdata.thresholds")}
          actions={
            <div className="flex flex-wrap items-center gap-2">
              <Select value={domain} onChange={(e) => setDomain(e.target.value as ApprovalDomain | "")} className="w-52" aria-label={t("refdata.th.col.domain")}>
                <option value="">{t("refdata.th.domainAll")}</option>
                {APPROVAL_DOMAINS.map((d) => <option key={d} value={d}>{t(`refdata.domain.${d}`)}</option>)}
              </Select>
              <Checkbox label={t("records.includeRemoved")} checked={showRetired} onChange={(e) => setShowRetired(e.target.checked)} />
              <Button icon={<Plus className="size-4" />} onClick={() => setEditing("new")}>{t("refdata.th.new")}</Button>
            </div>
          }
        />
        {query.isError ? (
          <ErrorState error={query.error} retry={() => query.refetch()} />
        ) : (
          <DataTable columns={columns} rows={rows} rowKey={(r) => r.id} loading={query.isLoading} empty={<EmptyState icon={<Scale />} title={t("refdata.th.empty")} />} />
        )}
      </Card>
      {domain ? <TryAmount domain={domain} /> : null}
      <ThresholdModal open={editing !== null} item={editing === "new" ? null : editing} defaultDomain={domain || "sales_order"} onClose={() => setEditing(null)} />
      <RemoveDialog
        open={!!deleting}
        onClose={() => setDeleting(null)}
        onConfirm={(reason) => deleting && remove.mutate({ id: deleting.id, reason })}
        title={t("refdata.th.delete")}
        message={deleting ? t("refdata.th.deleteConfirm", { level: deleting.level, domain: t(`refdata.domain.${deleting.domain}`) }) : undefined}
        confirmLabel={t("refdata.th.delete")}
        loading={remove.isPending}
        error={remove.error}
        onlineOnly
      />
    </div>
  );
}

/** Live preview of approvals.Levels for the selected domain. */
function TryAmount({ domain }: { domain: ApprovalDomain }) {
  const { t, tx } = useT();
  const [amount, setAmount] = useState("1000000");
  const [levels, setLevels] = useState<ApprovalLevel[] | null>(null);
  useEffect(() => {
    const n = Number(amount);
    if (!Number.isFinite(n) || n < 0) return;
    const id = setTimeout(() => void fetchLevels(domain, Math.round(n)).then(setLevels).catch(() => setLevels(null)), 250);
    return () => clearTimeout(id);
  }, [domain, amount]);
  return (
    <Card className="p-4">
      <div className="flex flex-wrap items-end gap-4">
        <Field label={t("refdata.th.try")} htmlFor="th-try" className="w-56">
          <Input id="th-try" inputMode="numeric" value={amount} onChange={(e) => setAmount(e.target.value.replace(/[^\d]/g, ""))} />
        </Field>
        <p className="pb-2 text-sm text-slate-700" aria-live="polite">
          {levels == null ? null : levels.length === 0 ? t("refdata.th.tryNone") : t("refdata.th.tryResult", { levels: levels.map((l) => `L${l.level} ${l.role ? tx(`enums.role.${l.role}`, undefined, l.role) : l.permission}`).join(" → ") })}
        </p>
      </div>
    </Card>
  );
}

interface ThForm { domain: ApprovalDomain; level: string; minAmount: string; maxAmount: string; permission: string; role: string; label: string; isActive: boolean }

function ThresholdModal({ open, item, defaultDomain, onClose }: { open: boolean; item: Threshold | null; defaultDomain: ApprovalDomain; onClose: () => void }) {
  const { t, tx } = useT();
  const { roles } = useRoleOptions();
  const [f, setF] = useState<ThForm>({ domain: defaultDomain, level: "1", minAmount: "0", maxAmount: "", permission: "sales.approve", role: "", label: "", isActive: true });
  const [touched, setTouched] = useState(false);
  useEffect(() => {
    if (!open) return;
    setTouched(false);
    setF(item
      ? { domain: item.domain, level: String(item.level), minAmount: String(item.minAmount), maxAmount: item.maxAmount == null ? "" : String(item.maxAmount), permission: item.permission, role: item.role ?? "", label: item.label, isActive: item.isActive }
      : { domain: defaultDomain, level: "1", minAmount: "0", maxAmount: "", permission: APPROVAL_PERMISSIONS[APPROVAL_DOMAINS.indexOf(defaultDomain)] ?? "sales.approve", role: "", label: "", isActive: true });
  }, [open, item, defaultDomain]);

  const save = useMutate(
    (body: Record<string, unknown>) => (item ? updateThreshold({ id: item.id, body }) : createThreshold(body)),
    { success: t("refdata.th.saved"), invalidate: INVALIDATE, onSuccess: () => onClose() },
  );
  const min = Number(f.minAmount), max = f.maxAmount === "" ? null : Number(f.maxAmount);
  const errors: Record<string, string> = {};
  if (f.minAmount === "" || !Number.isInteger(min) || min < 0) errors.minAmount = t("refdata.required");
  if (max != null && (!Number.isInteger(max) || max <= min)) errors.maxAmount = t("refdata.th.maxGtMin");
  const err = (k: string) => (touched ? errors[k] : undefined) ?? fieldError(save.error, k);
  const num = (s: string) => s.replace(/[^\d]/g, "");

  function submit(e: FormEvent) {
    e.preventDefault();
    setTouched(true);
    if (Object.keys(errors).length) return;
    save.mutate({ domain: f.domain, level: Number(f.level), minAmount: min, maxAmount: max, permission: f.permission, role: f.role || null, label: f.label.trim(), isActive: f.isActive });
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={item ? t("refdata.th.edit") : t("refdata.th.new")}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
          <Button type="submit" form="th-form" loading={save.isPending}>{item ? t("common.saveChanges") : t("refdata.create")}</Button>
        </>
      }
    >
      <form id="th-form" onSubmit={submit} noValidate className="grid gap-4 sm:grid-cols-2">
        {save.error && !["minAmount", "maxAmount", "permission", "role"].some((k) => fieldError(save.error, k)) ? <div className="sm:col-span-2"><Alert tone="red">{save.error.message}</Alert></div> : null}
        <Field label={t("refdata.th.col.domain")} required htmlFor="th-domain">
          <Select id="th-domain" value={f.domain} onChange={(e) => setF((s) => ({ ...s, domain: e.target.value as ApprovalDomain }))}>
            {APPROVAL_DOMAINS.map((d) => <option key={d} value={d}>{t(`refdata.domain.${d}`)}</option>)}
          </Select>
        </Field>
        <Field label={t("refdata.th.col.level")} required htmlFor="th-level">
          <Select id="th-level" value={f.level} onChange={(e) => setF((s) => ({ ...s, level: e.target.value }))}>
            {[1, 2, 3, 4, 5].map((n) => <option key={n} value={n}>L{n}</option>)}
          </Select>
        </Field>
        <Field label={t("refdata.th.field.min")} required error={err("minAmount")} htmlFor="th-min">
          <Input id="th-min" inputMode="numeric" value={f.minAmount} onChange={(e) => setF((s) => ({ ...s, minAmount: num(e.target.value) }))} invalid={!!err("minAmount")} className="tabular-nums" />
        </Field>
        <Field label={t("refdata.th.field.max")} error={err("maxAmount")} hint={t("refdata.th.field.maxHint")} htmlFor="th-max">
          <Input id="th-max" inputMode="numeric" value={f.maxAmount} onChange={(e) => setF((s) => ({ ...s, maxAmount: num(e.target.value) }))} invalid={!!err("maxAmount")} className="tabular-nums" />
        </Field>
        <Field label={t("refdata.th.field.permission")} required error={err("permission")} htmlFor="th-perm">
          <Select id="th-perm" value={f.permission} onChange={(e) => setF((s) => ({ ...s, permission: e.target.value }))} className="font-mono text-xs">
            {[...new Set([...APPROVAL_PERMISSIONS, f.permission])].map((p) => <option key={p} value={p}>{p}</option>)}
          </Select>
        </Field>
        <Field label={t("refdata.th.field.role")} error={err("role")} htmlFor="th-role">
          <Select id="th-role" value={f.role} onChange={(e) => setF((s) => ({ ...s, role: e.target.value }))}>
            <option value="">{t("refdata.th.field.roleAny")}</option>
            {roles.map((r) => <option key={r} value={r}>{tx(`enums.role.${r}`, undefined, r)}</option>)}
          </Select>
        </Field>
        <Field label={t("refdata.th.field.label")} htmlFor="th-label" className="sm:col-span-2">
          <Input id="th-label" value={f.label} maxLength={160} onChange={(e) => setF((s) => ({ ...s, label: e.target.value }))} />
        </Field>
        <div className="sm:col-span-2">
          <Checkbox label={t("refdata.active")} checked={f.isActive} onChange={(e) => setF((s) => ({ ...s, isActive: e.target.checked }))} />
        </div>
      </form>
    </Modal>
  );
}

const ROLES = ["MANAGING_DIRECTOR", "GENERAL_MANAGER", "OPERATIONS_ADVISOR", "PRODUCTION_MANAGER", "SALES_MANAGER", "FINANCE_MANAGER", "PROCUREMENT_OFFICER", "STOCK_OFFICER", "SUPERVISOR", "SYSTEM_ADMIN"];
function useRoleOptions() {
  return { roles: ROLES };
}
