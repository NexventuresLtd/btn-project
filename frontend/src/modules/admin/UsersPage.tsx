import { useEffect, useState, type FormEvent } from "react";
import { KeyRound, LogOut, Pencil, Power, UserPlus, Unlock as UnlockIcon, Users } from "lucide-react";
import { toast } from "sonner";
import {
  Alert, Badge, Button, Card, Checkbox, ConfirmDialog, DataTable, EmptyState, ErrorState, Field, Input, Modal, PageHeader, Pagination, Select, StatusBadge, type Column,
} from "@/components/ui";
import { useT } from "@/i18n";
import { useAuth } from "@/auth/AuthProvider";
import { useLookups } from "@/hooks/useLookups";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { formatDateTime } from "@/lib/dates";
import {
  ADMIN_KEY, createUser, isLocked, isPlantScoped, resetPassword, revokeSessions, unlockUser, updateUser, useUsers, type AdminUser, type UserPayload,
} from "./api";
import { CopyBox, RowMenu, SearchInput, useDebounced } from "./components";

const USERS_KEY = [...ADMIN_KEY, "users"];

type Pending =
  | { kind: "reset" | "revoke" | "toggle"; user: AdminUser }
  | null;

export function UsersPage() {
  const { t, tx } = useT();
  const { user: me } = useAuth();
  const { data: lookups } = useLookups();
  const [search, setSearch] = useState("");
  const q = useDebounced(search.trim());
  const [page, setPage] = useState(1);
  useEffect(() => setPage(1), [q]);
  const users = useUsers({ q, page });

  const [editing, setEditing] = useState<AdminUser | "new" | null>(null);
  const [pending, setPending] = useState<Pending>(null);
  const [secret, setSecret] = useState<{ title: string; name: string; email: string; password: string } | null>(null);

  const roleLabel = (role: string) => tx(`enums.role.${role}`, undefined, lookups?.roles.find((r) => r.value === role)?.label ?? role);

  const reset = useMutate((u: AdminUser) => resetPassword(u.id).then((r) => ({ ...r, u })), {
    invalidate: [USERS_KEY],
    onSuccess: (r) => {
      setPending(null);
      if (r.emailed || !r.tempPassword) toast.success(t("admin.users.toast.resetEmailed", { email: r.u.email }));
      else setSecret({ title: t("admin.users.secret.resetTitle"), name: r.u.name, email: r.u.email, password: r.tempPassword });
    },
  });
  const unlock = useMutate((u: AdminUser) => unlockUser(u.id), { success: t("admin.users.toast.unlocked"), invalidate: [USERS_KEY] });
  const revoke = useMutate((u: AdminUser) => revokeSessions(u.id), { success: t("admin.users.toast.signedOutAll"), invalidate: [USERS_KEY], onSuccess: () => setPending(null) });
  const toggle = useMutate((u: AdminUser) => updateUser({ id: u.id, body: { isActive: !u.isActive } }), {
    success: (r) => (r.user.isActive ? t("admin.users.toast.reactivated", { name: r.user.name }) : t("admin.users.toast.deactivated", { name: r.user.name })),
    invalidate: [USERS_KEY],
    onSuccess: () => setPending(null),
  });

  const columns: Column<AdminUser>[] = [
    {
      key: "name",
      header: t("admin.users.col.user"),
      cell: (u) => (
        <div className="min-w-0">
          <p className="truncate font-semibold text-slate-900">
            {u.name}
            {u.id === me?.id ? <span className="ml-1.5 text-xs font-medium text-slate-400">{t("admin.users.you")}</span> : null}
          </p>
          <p className="truncate text-xs text-slate-500">{u.email}</p>
        </div>
      ),
    },
    { key: "role", header: t("admin.users.col.role"), cell: (u) => <span className="whitespace-nowrap">{roleLabel(u.role)}</span> },
    { key: "plant", header: t("common.plant"), hideOnMobile: true, cell: (u) => (u.plant ? <Badge tone="navy">{u.plant.code}</Badge> : <span className="text-slate-400">{t("common.allPlants")}</span>) },
    {
      key: "status",
      header: t("common.status"),
      cell: (u) => (
        <div className="flex flex-wrap gap-1.5">
          <StatusBadge status={u.isActive ? "ACTIVE" : "INACTIVE"} />
          {isLocked(u) ? <StatusBadge status="LOCKED" /> : null}
          {u.mustChangePassword && u.isActive ? <Badge tone="amber">{t("admin.users.mustChange")}</Badge> : null}
        </div>
      ),
    },
    { key: "last", header: t("admin.users.col.lastSignIn"), hideOnMobile: true, cell: (u) => <span className="whitespace-nowrap text-slate-600">{u.lastLoginAt ? formatDateTime(u.lastLoginAt) : <span className="text-slate-400">{t("admin.users.never")}</span>}</span> },
    {
      key: "actions",
      header: <span className="sr-only">{t("common.actions")}</span>,
      align: "right",
      className: "w-12",
      cell: (u) => (
        <RowMenu
          label={t("admin.users.actionsFor", { name: u.name })}
          items={[
            { label: t("admin.users.menu.edit"), icon: <Pencil />, onClick: () => setEditing(u) },
            { label: t("admin.users.menu.reset"), icon: <KeyRound />, onClick: () => setPending({ kind: "reset", user: u }), hidden: !u.isActive },
            { label: t("admin.users.menu.unlock"), icon: <UnlockIcon />, onClick: () => unlock.mutate(u), hidden: !isLocked(u) },
            { label: t("admin.users.menu.revoke"), icon: <LogOut />, onClick: () => setPending({ kind: "revoke", user: u }) },
            { label: u.isActive ? t("admin.users.menu.deactivate") : t("admin.users.menu.activate"), icon: <Power />, tone: u.isActive ? "danger" : "default", onClick: () => setPending({ kind: "toggle", user: u }), hidden: u.id === me?.id },
          ]}
        />
      ),
    },
  ];

  const p = pending;
  return (
    <>
      <PageHeader
        title={t("admin.users.title")}
        description={t("admin.users.description")}
        breadcrumbs={[{ label: t("admin.breadcrumb") }, { label: t("admin.users.title") }]}
        actions={<Button icon={<UserPlus className="size-4" />} onClick={() => setEditing("new")}>{t("admin.users.new")}</Button>}
      />
      <Card>
        <div className="flex flex-wrap items-center justify-between gap-3 px-5 py-4">
          <SearchInput value={search} onChange={setSearch} placeholder={t("admin.users.search")} className="w-full sm:w-80" />
          {users.data ? <p className="text-xs text-slate-500">{t("admin.users.count", { count: users.data.total })}</p> : null}
        </div>
        {users.isError ? (
          <ErrorState error={users.error} retry={() => users.refetch()} />
        ) : !users.isLoading && !users.data?.items.length && !q ? (
          <EmptyState icon={<Users />} title={t("admin.users.emptyTitle")} description={t("admin.users.emptyText")} action={<Button onClick={() => setEditing("new")}>{t("admin.users.new")}</Button>} />
        ) : (
          <>
            <DataTable columns={columns} rows={users.data?.items} rowKey={(u) => u.id} loading={users.isLoading} empty={t("admin.users.noMatch", { q })} />
            {users.data ? <Pagination page={users.data.page} pageSize={users.data.pageSize} total={users.data.total} onPage={setPage} /> : null}
          </>
        )}
      </Card>

      <UserModal
        open={editing !== null}
        user={editing === "new" ? null : editing}
        isSelf={editing !== null && editing !== "new" && editing.id === me?.id}
        onClose={() => setEditing(null)}
        onCreated={(r) => {
          if (r.emailed || !r.tempPassword) toast.success(t("admin.users.toast.created", { name: r.user.name, email: r.user.email }));
          else setSecret({ title: t("admin.users.secret.createdTitle"), name: r.user.name, email: r.user.email, password: r.tempPassword });
        }}
      />

      <ConfirmDialog
        open={p?.kind === "reset"}
        onClose={() => setPending(null)}
        onConfirm={() => p && reset.mutate(p.user)}
        loading={reset.isPending}
        title={t("admin.users.reset.title")}
        confirmLabel={t("admin.users.menu.reset")}
        message={p ? <>{t("admin.users.reset.message", { email: p.user.email })}</> : null}
      />
      <ConfirmDialog
        open={p?.kind === "revoke"}
        onClose={() => setPending(null)}
        onConfirm={() => p && revoke.mutate(p.user)}
        loading={revoke.isPending}
        title={t("admin.users.revoke.title")}
        confirmLabel={t("admin.users.revoke.confirm")}
        message={p ? <>{t("admin.users.revoke.message", { name: p.user.name })}</> : null}
      />
      <ConfirmDialog
        open={p?.kind === "toggle"}
        onClose={() => setPending(null)}
        onConfirm={() => p && toggle.mutate(p.user)}
        loading={toggle.isPending}
        tone={p?.user.isActive ? "danger" : "primary"}
        title={p?.user.isActive ? t("admin.users.deactivate.title") : t("admin.users.activate.title")}
        confirmLabel={p?.user.isActive ? t("admin.users.menu.deactivate") : t("admin.users.menu.activate")}
        message={
          p ? (
            p.user.isActive ? (
              <>{t("admin.users.deactivate.message", { name: p.user.name })}</>
            ) : (
              <>{t("admin.users.activate.message", { name: p.user.name })}</>
            )
          ) : null
        }
      />

      <Modal open={!!secret} onClose={() => setSecret(null)} title={secret?.title ?? ""} size="sm" footer={<Button onClick={() => setSecret(null)}>{t("common.done")}</Button>}>
        {secret ? (
          <div className="space-y-4">
            <Alert tone="amber" title={t("admin.users.secret.alertTitle")}>
              {t("admin.users.secret.alertText", { email: secret.email, name: secret.name })}
            </Alert>
            <CopyBox value={secret.password} />
          </div>
        ) : null}
      </Modal>
    </>
  );
}

interface FormState {
  name: string;
  email: string;
  phone: string;
  role: string;
  plantId: string;
  isActive: boolean;
}

function UserModal({ open, user, isSelf, onClose, onCreated }: {
  open: boolean;
  user: AdminUser | null;
  isSelf: boolean;
  onClose: () => void;
  onCreated: (r: { user: AdminUser; emailed: boolean; tempPassword?: string }) => void;
}) {
  const { t, tx } = useT();
  const { user: me } = useAuth();
  const { data: lookups } = useLookups();
  const [form, setForm] = useState<FormState>({ name: "", email: "", phone: "", role: "", plantId: "", isActive: true });
  const [touched, setTouched] = useState(false);

  useEffect(() => {
    if (!open) return;
    setTouched(false);
    setForm(user ? { name: user.name, email: user.email, phone: user.phone ?? "", role: user.role, plantId: user.plant?.id ?? "", isActive: user.isActive } : { name: "", email: "", phone: "", role: "", plantId: "", isActive: true });
  }, [open, user]);

  const save = useMutate(
    (body: UserPayload) => (user ? updateUser({ id: user.id, body: { name: body.name, phone: body.phone, role: body.role, plantId: body.plantId, isActive: body.isActive } }).then((r) => ({ user: r.user, emailed: true })) : createUser(body)),
    {
      invalidate: [USERS_KEY],
      success: user ? (r) => t("admin.users.toast.updated", { name: r.user.name }) : undefined,
      onSuccess: (r) => {
        onClose();
        if (!user) onCreated(r);
      },
    },
  );

  const scoped = isPlantScoped(form.role);
  const errors: Partial<Record<keyof FormState, string>> = {};
  if (form.name.trim().length < 2) errors.name = t("admin.users.err.name");
  if (!user && !/^\S+@\S+\.\S+$/.test(form.email.trim())) errors.email = t("admin.users.err.email");
  if (!form.role) errors.role = t("admin.users.err.role");
  if (scoped && !form.plantId) errors.plantId = t("admin.users.err.plant");
  const err = (k: keyof FormState) => (touched ? errors[k] : undefined) ?? fieldError(save.error, k);
  const set = <K extends keyof FormState>(k: K, v: FormState[K]) => setForm((f) => ({ ...f, [k]: v }));

  function submit(e: FormEvent) {
    e.preventDefault();
    setTouched(true);
    if (Object.keys(errors).length) return;
    save.mutate({
      name: form.name.trim(),
      email: form.email.trim().toLowerCase(),
      phone: form.phone.trim() || null,
      role: form.role,
      plantId: scoped ? form.plantId : null,
      isActive: form.isActive,
    });
  }

  const roleOptions = (lookups?.roles ?? []).filter((r) => r.value !== "SUPER_ADMIN" || me?.role === "SUPER_ADMIN" || user?.role === "SUPER_ADMIN");
  const withSuper = me?.role === "SUPER_ADMIN" && !roleOptions.some((r) => r.value === "SUPER_ADMIN") ? [...roleOptions, { value: "SUPER_ADMIN", label: "Super Administrator" }] : roleOptions;
  const roleChanged = !!user && form.role !== user.role;
  return (
    <Modal
      open={open}
      onClose={onClose}
      title={user ? t("admin.users.modal.edit") : t("admin.users.new")}
      description={user ? user.email : t("admin.users.modal.newHint")}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
          <Button type="submit" form="user-form" loading={save.isPending}>{user ? t("common.saveChanges") : t("admin.users.modal.create")}</Button>
        </>
      }
    >
      <form id="user-form" onSubmit={submit} noValidate className="grid gap-4 sm:grid-cols-2">
        {save.error && !fieldError(save.error, "name") && !fieldError(save.error, "email") && !fieldError(save.error, "plantId") ? (
          <div className="sm:col-span-2"><Alert tone="red">{save.error.message}</Alert></div>
        ) : null}
        <Field label={t("admin.users.field.name")} required error={err("name")} htmlFor="u-name" className="sm:col-span-2">
          <Input id="u-name" value={form.name} onChange={(e) => set("name", e.target.value)} invalid={!!err("name")} autoFocus autoComplete="off" />
        </Field>
        <Field label={t("admin.users.field.email")} required={!user} error={err("email")} htmlFor="u-email" hint={user ? t("admin.users.field.emailHint") : undefined}>
          <Input id="u-email" type="email" value={form.email} onChange={(e) => set("email", e.target.value)} disabled={!!user} invalid={!!err("email")} autoComplete="off" placeholder={t("admin.users.field.emailPh")} />
        </Field>
        <Field label={t("admin.users.field.phone")} error={err("phone")} htmlFor="u-phone">
          <Input id="u-phone" type="tel" inputMode="tel" value={form.phone} onChange={(e) => set("phone", e.target.value)} placeholder={t("admin.users.field.phonePh")} />
        </Field>
        <Field label={t("admin.users.col.role")} required error={err("role")} htmlFor="u-role" hint={isSelf ? t("admin.users.field.roleSelf") : undefined}>
          <Select id="u-role" value={form.role} onChange={(e) => set("role", e.target.value)} disabled={isSelf} invalid={!!err("role")}>
            <option value="" disabled>{t("admin.users.field.roleChoose")}</option>
            {withSuper.map((r) => <option key={r.value} value={r.value}>{tx(`enums.role.${r.value}`, undefined, r.label)}</option>)}
          </Select>
        </Field>
        <Field label={t("common.plant")} required={scoped} error={err("plantId")} htmlFor="u-plant" hint={scoped ? t("admin.users.field.plantScoped") : t("admin.users.field.plantAll")}>
          <Select id="u-plant" value={scoped ? form.plantId : ""} onChange={(e) => set("plantId", e.target.value)} disabled={!scoped} invalid={!!err("plantId")}>
            <option value="">{scoped ? t("admin.users.field.plantChoose") : t("common.allPlants")}</option>
            {lookups?.plants.map((p) => <option key={p.id} value={p.id}>{p.code} — {p.name}</option>)}
          </Select>
        </Field>
        {user ? (
          <div className="sm:col-span-2">
            <Checkbox label={t("admin.users.field.active")} checked={form.isActive} disabled={isSelf} onChange={(e) => set("isActive", e.target.checked)} />
            {roleChanged || (user.isActive && !form.isActive) ? (
              <p className="mt-2 text-xs text-amber-700">{roleChanged ? t("admin.users.saveSignsOutRole") : t("admin.users.saveSignsOut")}</p>
            ) : null}
          </div>
        ) : null}
      </form>
    </Modal>
  );
}
