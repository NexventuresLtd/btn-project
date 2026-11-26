import { useEffect, useState, type FormEvent } from "react";
import { Alert, Button, Field, Input, Modal, Select, DateInput } from "@/components/ui";
import { useT, type T } from "@/i18n";
import { useLookups } from "@/hooks/useLookups";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { ApiError } from "@/lib/api";
import { useAuth } from "@/auth/AuthProvider";
import { toast } from "sonner";
import { OfflineLockedError, useConnectivity, useOfflineMutation } from "@/offline/hooks";
import { useQueryClient } from "@tanstack/react-query";
import { addToOfflineRoster, WORKER_REGISTRATION_TYPE } from "./offline";
import { createWorker, DEFAULT_WAGE, EMPLOYMENT_STATUSES, PAYROLL_KEY, RATE_BASES, updateWorker, type EmploymentStatus, type RateBasis, type Worker, type WorkerPayload } from "./api";
import { momoNameMismatch, normaliseMomo } from "./identity";

interface FormState {
  firstName: string;
  lastName: string;
  nationalId: string;
  momoNumber: string;
  momoName: string;
  jobTitle: string;
  plantId: string;
  dailyWage: string;
  startDate: string;
  phone: string;
  rateBasis: RateBasis;
  employmentStatus: EmploymentStatus;
  leftDate: string;
}

const empty = (plantId = ""): FormState => ({ firstName: "", lastName: "", nationalId: "", momoNumber: "", momoName: "", jobTitle: "Helper", plantId, dailyWage: "", startDate: "",
  phone: "", rateBasis: "DAILY", employmentStatus: "ACTIVE", leftDate: "" });

const fromWorker = (w: Worker): FormState => ({
  firstName: w.firstName,
  lastName: w.lastName,
  nationalId: w.nationalId ?? "",
  momoNumber: w.momoNumber ?? "",
  momoName: w.momoName ?? "",
  jobTitle: w.jobTitle,
  plantId: w.plantId,
  dailyWage: String(w.dailyWage),
  startDate: w.startDate ?? "",
  phone: w.phone ?? "",
  rateBasis: w.rateBasis ?? "DAILY",
  employmentStatus: w.employmentStatus ?? (w.isActive ? "ACTIVE" : "INACTIVE"),
  leftDate: w.leftDate ?? "",
});

function validate(f: FormState, t: T): Record<string, string> {
  const e: Record<string, string> = {};
  if (!f.firstName.trim()) e.firstName = t("payroll.worker.err.firstName");
  if (!f.lastName.trim()) e.lastName = t("payroll.worker.err.lastName");
  if (!/^\d{16}$/.test(f.nationalId.replace(/\s+/g, ""))) e.nationalId = t("payroll.worker.err.nationalId");
  if (!normaliseMomo(f.momoNumber)) e.momoNumber = t("payroll.worker.err.momo");
  if (!f.jobTitle.trim()) e.jobTitle = t("payroll.worker.err.jobTitle");
  if (!f.plantId) e.plantId = t("payroll.worker.err.plant");
  if (f.phone.trim() && !normaliseMomo(f.phone)) e.phone = t("payroll.wrk.err.phone");
  const wage = Number(f.dailyWage);
  const pieceOk = f.rateBasis === "PIECE" && f.dailyWage.trim() !== "" && wage === 0;
  if (pieceOk) return e;
  if (!f.dailyWage.trim() || !Number.isFinite(wage) || wage <= 0) e.dailyWage = t(f.rateBasis === "PIECE" ? "payroll.wrk.err.wagePiece" : "payroll.worker.err.wagePositive");
  else if (!Number.isInteger(wage)) e.dailyWage = t("payroll.worker.err.wholeFrancs");
  return e;
}

/** Register or edit a casual worker. */
export function WorkerModal({ open, onClose, worker, pinnedPlant, defaultPlant }: { open: boolean; onClose: () => void; worker: Worker | null; pinnedPlant: string | null; defaultPlant?: string }) {
  const { t, tx } = useT();
  const { data: lookups } = useLookups();
  const { can } = useAuth();
  // WRK-01: a site supervisor registers at his own site; Finance approves it (the record stays PENDING until then)
  const registrar = !can("workers.manage") && can("workers.register");
  const offline = useOfflineMutation<WorkerPayload>(WORKER_REGISTRATION_TYPE);
  const net = useConnectivity();
  const qc = useQueryClient();
  const [form, setForm] = useState<FormState>(() => empty());
  const [touched, setTouched] = useState(false);

  useEffect(() => {
    if (!open) return;
    if (worker) setForm(fromWorker(worker));
    else {
      const plantId = pinnedPlant ?? defaultPlant ?? "";
      const code = lookups?.plants.find((p) => p.id === plantId)?.code;
      setForm({ ...empty(plantId), dailyWage: code && DEFAULT_WAGE[code] ? String(DEFAULT_WAGE[code]) : "" });
    }
    setTouched(false);
  }, [open, worker, pinnedPlant, defaultPlant, lookups]);

  const save = useMutate((body: WorkerPayload) => (worker ? updateWorker({ id: worker.id, body }) : createWorker(body)), {
    success: (w) => t(worker ? "payroll.worker.updated" : "payroll.worker.registered", { name: w.fullName }),
    invalidate: [[...PAYROLL_KEY, "workers"]],
    onSuccess: () => onClose(),
  });

  const errors = validate(form, t);
  const err = (k: keyof FormState) => (touched ? errors[k] : undefined) ?? fieldError(save.error, k);
  const set = <K extends keyof FormState>(k: K, v: FormState[K]) => setForm((f) => ({ ...f, [k]: v }));

  const onPlant = (plantId: string) => {
    setForm((f) => {
      const code = lookups?.plants.find((p) => p.id === plantId)?.code;
      const prevCode = lookups?.plants.find((p) => p.id === f.plantId)?.code;
      // follow the plant's usual wage unless the user typed their own
      const followsDefault = !f.dailyWage || (prevCode && f.dailyWage === String(DEFAULT_WAGE[prevCode]));
      return { ...f, plantId, dailyWage: !worker && followsDefault && code && DEFAULT_WAGE[code] ? String(DEFAULT_WAGE[code]) : f.dailyWage };
    });
  };

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setTouched(true);
    if (Object.keys(errors).length) return;
    const body: WorkerPayload = {
      firstName: form.firstName,
      lastName: form.lastName,
      nationalId: form.nationalId.replace(/\s+/g, ""),
      momoNumber: form.momoNumber,
      momoName: form.momoName.trim() || null,
      jobTitle: form.jobTitle,
      plantId: form.plantId,
      dailyWage: Number(form.dailyWage),
      startDate: form.startDate || null,
      phone: form.phone.trim() || null,
      rateBasis: form.rateBasis,
      employmentStatus: form.employmentStatus,
      leftDate: form.employmentStatus === "LEFT" ? form.leftDate || null : null,
    };
    if (registrar && !worker) void registerLocalFirst(body);
    else save.mutate(body);
  };

  // local-first: the registration is saved on the device (so the worker can be marked present offline) and syncs
  const registerLocalFirst = async (body: WorkerPayload) => {
    try {
      const r = await offline.submit(body, { quiet: true });
      await addToOfflineRoster(r.uuid, body);
      toast.success(t("payroll.reg.saved", { name: `${body.firstName} ${body.lastName}`.toUpperCase() }));
      onClose();
    } catch (e) {
      if (e instanceof OfflineLockedError && net.reachable) save.mutate(body);
      else if (!(e instanceof OfflineLockedError)) toast.error((e as Error).message);
    } finally {
      void qc.invalidateQueries({ queryKey: [...PAYROLL_KEY, "workers"] });
    }
  };

  const mismatch = momoNameMismatch({ firstName: form.firstName, lastName: form.lastName, momoName: form.momoName });
  const apiMessage = save.error instanceof ApiError && !save.error.fields ? save.error.message : null;
  const wageChanged = worker && form.dailyWage !== String(worker.dailyWage);

  return (
    <Modal
      open={open}
      onClose={onClose}
      size="lg"
      title={worker ? t("payroll.worker.title.edit", { name: worker.fullName }) : t("payroll.worker.title.new")}
      description={registrar && !worker ? t("payroll.reg.description") : t("payroll.worker.description")}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
          <Button type="submit" form="worker-form" loading={save.isPending || offline.isSaving}>{worker ? t("common.saveChanges") : t("payroll.worker.register")}</Button>
        </>
      }
    >
      <form id="worker-form" onSubmit={submit} className="space-y-4" noValidate>
        {apiMessage ? <Alert tone="red">{apiMessage}</Alert> : null}
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t("payroll.worker.firstName")} required error={err("firstName")} htmlFor="wk-first">
            <Input id="wk-first" value={form.firstName} onChange={(e) => set("firstName", e.target.value)} invalid={!!err("firstName")} autoComplete="off" className="uppercase" />
          </Field>
          <Field label={t("payroll.worker.lastName")} required error={err("lastName")} htmlFor="wk-last">
            <Input id="wk-last" value={form.lastName} onChange={(e) => set("lastName", e.target.value)} invalid={!!err("lastName")} autoComplete="off" className="uppercase" />
          </Field>
          <Field label={t("payroll.worker.nationalId")} required error={err("nationalId")} hint={t("payroll.worker.nationalIdHint")} htmlFor="wk-nid">
            <Input id="wk-nid" value={form.nationalId} onChange={(e) => set("nationalId", e.target.value)} invalid={!!err("nationalId")} inputMode="numeric" maxLength={20} className="tabular" />
          </Field>
          <Field label={t("payroll.worker.jobTitle")} required error={err("jobTitle")} htmlFor="wk-job">
            <Input id="wk-job" value={form.jobTitle} onChange={(e) => set("jobTitle", e.target.value)} invalid={!!err("jobTitle")} list="wk-job-titles" />
            <datalist id="wk-job-titles">
              <option value="Helper" />
              <option value="Mason" />
              <option value="Loader" />
              <option value="Machine helper" />
              <option value="Watchman" />
            </datalist>
          </Field>
          <Field label={t("payroll.worker.momoNumber")} required error={err("momoNumber")} hint={t("payroll.worker.momoNumberHint")} htmlFor="wk-momo">
            <Input id="wk-momo" value={form.momoNumber} onChange={(e) => set("momoNumber", e.target.value)} invalid={!!err("momoNumber")} inputMode="tel" className="tabular" />
          </Field>
          <Field label={t("payroll.worker.momoName")} error={err("momoName")} hint={t("payroll.worker.momoNameHint")} htmlFor="wk-momoname">
            <Input id="wk-momoname" value={form.momoName} onChange={(e) => set("momoName", e.target.value)} invalid={!!err("momoName")} autoComplete="off" className="uppercase" />
          </Field>
        </div>
        {mismatch ? (
          <Alert tone="amber" title={t("payroll.worker.mismatchTitle")}>
            {t("payroll.worker.mismatchText", { momo: form.momoName.trim().toUpperCase(), name: `${form.firstName.toUpperCase()} ${form.lastName.toUpperCase()}` })}
          </Alert>
        ) : null}
        <div className="grid gap-4 sm:grid-cols-3">
          <Field label={t("common.plant")} required error={err("plantId")} htmlFor="wk-plant">
            <Select id="wk-plant" value={form.plantId} onChange={(e) => onPlant(e.target.value)} disabled={!!pinnedPlant || (registrar && !!worker)} invalid={!!err("plantId")}>
              {!pinnedPlant ? <option value="">{t("payroll.newPeriod.choosePlantOption")}</option> : null}
              {lookups?.plants.map((p) => <option key={p.id} value={p.id}>{p.code}</option>)}
            </Select>
          </Field>
          <Field label={t("payroll.worker.dailyWage")} required error={err("dailyWage")} hint={wageChanged ? t("payroll.worker.wageHint") : undefined} htmlFor="wk-wage">
            <Input id="wk-wage" value={form.dailyWage} onChange={(e) => set("dailyWage", e.target.value)} invalid={!!err("dailyWage")} inputMode="numeric" className="tabular text-right" />
          </Field>
          <Field label={t("payroll.worker.startDate")} htmlFor="wk-start">
            <DateInput id="wk-start" clearable value={form.startDate} onChange={(v) => set("startDate", v)} />
          </Field>
        </div>
        <div className="grid gap-4 sm:grid-cols-3">
          <Field label={t("payroll.wrk.phone")} error={err("phone")} hint={t("payroll.wrk.phoneHint")} htmlFor="wk-phone">
            <Input id="wk-phone" value={form.phone} onChange={(e) => set("phone", e.target.value)} invalid={!!err("phone")} inputMode="tel" autoComplete="off" className="tabular" />
          </Field>
          <Field label={t("payroll.wrk.rateBasis")} required error={err("rateBasis")} hint={tx(`payroll.wrk.basisHint.${form.rateBasis}`)} htmlFor="wk-basis">
            <Select id="wk-basis" value={form.rateBasis} onChange={(e) => set("rateBasis", e.target.value as RateBasis)}>
              {RATE_BASES.map((b) => <option key={b} value={b}>{tx(`payroll.wrk.basis.${b}`)}</option>)}
            </Select>
          </Field>
          <Field label={t("payroll.wrk.status")} required error={err("employmentStatus")} htmlFor="wk-status">
            <Select id="wk-status" value={form.employmentStatus} disabled={registrar} onChange={(e) => set("employmentStatus", e.target.value as EmploymentStatus)}>
              {EMPLOYMENT_STATUSES.map((s) => <option key={s} value={s}>{tx(`payroll.wrk.status.${s}`)}</option>)}
            </Select>
          </Field>
        </div>
        {form.employmentStatus === "LEFT" ? (
          <div className="grid gap-4 sm:grid-cols-3">
            <Field label={t("payroll.wrk.leftDate")} hint={t("payroll.wrk.leftDateHint")} error={err("leftDate")} htmlFor="wk-left">
              <DateInput id="wk-left" clearable value={form.leftDate} onChange={(v) => set("leftDate", v)} />
            </Field>
          </div>
        ) : null}
        <p className="text-xs text-slate-500">{t("payroll.wrk.privacy")}</p>
      </form>
    </Modal>
  );
}
