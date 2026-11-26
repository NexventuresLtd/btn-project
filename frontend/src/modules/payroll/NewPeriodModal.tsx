import { useEffect, useMemo, useState, type FormEvent } from "react";
import { useNavigate } from "react-router";
import { Alert, Button, Field, Modal, Select, DateInput } from "@/components/ui";
import { useT } from "@/i18n";
import { useLookups } from "@/hooks/useLookups";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { ApiError } from "@/lib/api";
import { todayISO } from "@/lib/dates";
import { cn } from "@/lib/cn";
import { createPeriod, PAYROLL_KEY } from "./api";

/** Localised short month names with Latin digits. */
function monthNames(locale: string): string[] {
  let fmt: Intl.DateTimeFormat;
  try {
    fmt = new Intl.DateTimeFormat(`${locale}-u-nu-latn`, { month: "short", timeZone: "UTC" });
  } catch {
    fmt = new Intl.DateTimeFormat("en-GB", { month: "short", timeZone: "UTC" });
  }
  return Array.from({ length: 12 }, (_, m) => fmt.format(new Date(Date.UTC(2024, m, 1))));
}

function lastDay(y: number, m: number) {
  return new Date(Date.UTC(y, m + 1, 0)).getUTCDate();
}
const iso = (y: number, m: number, d: number) => `${y}-${String(m + 1).padStart(2, "0")}-${String(d).padStart(2, "0")}`;

/** Half-month quick picks for the current and previous month, newest first. */
function quickPicks(today: string, MONTHS: string[]) {
  const y = Number(today.slice(0, 4));
  const m = Number(today.slice(5, 7)) - 1;
  const prev = m === 0 ? { y: y - 1, m: 11 } : { y, m: m - 1 };
  const out: { label: string; start: string; end: string }[] = [];
  for (const { y: yy, m: mm } of [{ y, m }, prev]) {
    const end = lastDay(yy, mm);
    out.push({ label: `16–${end} ${MONTHS[mm]}`, start: iso(yy, mm, 16), end: iso(yy, mm, end) });
    out.push({ label: `1–15 ${MONTHS[mm]}`, start: iso(yy, mm, 1), end: iso(yy, mm, 15) });
  }
  return out;
}

function defaultRange(today: string) {
  const y = Number(today.slice(0, 4));
  const m = Number(today.slice(5, 7)) - 1;
  const d = Number(today.slice(8, 10));
  return d <= 15 ? { start: iso(y, m, 1), end: iso(y, m, 15) } : { start: iso(y, m, 16), end: iso(y, m, lastDay(y, m)) };
}

const daysBetween = (a: string, b: string) => Math.round((Date.parse(`${b}T00:00:00Z`) - Date.parse(`${a}T00:00:00Z`)) / 86_400_000) + 1;

export function NewPeriodModal({ open, onClose, pinnedPlant, defaultPlant }: { open: boolean; onClose: () => void; pinnedPlant: string | null; defaultPlant?: string }) {
  const navigate = useNavigate();
  const { t, locale } = useT();
  const { data: lookups } = useLookups();
  const today = todayISO();
  const picks = useMemo(() => quickPicks(today, monthNames(locale)), [today, locale]);
  const [plantId, setPlantId] = useState("");
  const [range, setRange] = useState(() => defaultRange(today));
  const [touched, setTouched] = useState(false);

  useEffect(() => {
    if (!open) return;
    setPlantId(pinnedPlant ?? defaultPlant ?? (lookups?.plants.length === 1 ? lookups.plants[0].id : ""));
    setRange(defaultRange(today));
    setTouched(false);
  }, [open, pinnedPlant, defaultPlant, lookups, today]);

  const create = useMutate(createPeriod, {
    success: (p) => t("payroll.newPeriod.created", { number: p.number, workers: t("payroll.workers", { count: p.lines.length }) }),
    invalidate: [[...PAYROLL_KEY, "periods"]],
    onSuccess: (p) => {
      onClose();
      navigate(`/payroll/${p.id}`);
    },
  });

  const errors: Record<string, string> = {};
  if (!plantId) errors.plantId = t("payroll.newPeriod.err.plant");
  if (!range.start) errors.startDate = t("payroll.newPeriod.err.start");
  if (!range.end) errors.endDate = t("payroll.newPeriod.err.end");
  else if (range.start && range.end < range.start) errors.endDate = t("payroll.newPeriod.err.order");
  else if (range.start && daysBetween(range.start, range.end) > 31) errors.endDate = t("payroll.newPeriod.err.max");
  const err = (k: string) => (touched ? errors[k] : undefined) ?? fieldError(create.error, k);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setTouched(true);
    if (Object.keys(errors).length) return;
    create.mutate({ plantId, startDate: range.start, endDate: range.end });
  };

  const apiMessage = create.error instanceof ApiError && !create.error.fields ? create.error.message : null;
  const len = range.start && range.end && range.end >= range.start ? daysBetween(range.start, range.end) : null;

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t("payroll.newPeriod.title")}
      description={t("payroll.newPeriod.description")}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
          <Button type="submit" form="new-period" loading={create.isPending}>{t("payroll.newPeriod.create")}</Button>
        </>
      }
    >
      <form id="new-period" onSubmit={submit} className="space-y-4" noValidate>
        {apiMessage ? <Alert tone="red">{apiMessage}</Alert> : null}
        <Field label={t("common.plant")} required error={err("plantId")} htmlFor="np-plant">
          <Select id="np-plant" value={plantId} onChange={(e) => setPlantId(e.target.value)} disabled={!!pinnedPlant} invalid={!!err("plantId")}>
            {!pinnedPlant ? <option value="">{t("payroll.newPeriod.choosePlantOption")}</option> : null}
            {lookups?.plants.map((p) => <option key={p.id} value={p.id}>{p.code} · {p.name}</option>)}
          </Select>
        </Field>
        <div>
          <p className="mb-1.5 text-[13px] font-medium text-slate-700">{t("payroll.newPeriod.quickPick")}</p>
          <div className="flex flex-wrap gap-1.5">
            {picks.map((p) => {
              const active = p.start === range.start && p.end === range.end;
              return (
                <button
                  key={p.label + p.start}
                  type="button"
                  onClick={() => setRange({ start: p.start, end: p.end })}
                  className={cn(
                    "h-8 rounded-lg border px-3 text-xs font-semibold transition",
                    active ? "border-navy-800 bg-navy-800 text-white" : "border-line bg-white text-slate-600 hover:border-slate-300 hover:bg-slate-50",
                  )}
                >
                  {p.label} {p.start.slice(0, 4) !== today.slice(0, 4) ? p.start.slice(0, 4) : ""}
                </button>
              );
            })}
          </div>
        </div>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t("payroll.newPeriod.firstDay")} required error={err("startDate")} htmlFor="np-start">
            <DateInput id="np-start" value={range.start} onChange={(v) => setRange((r) => ({ ...r, start: v }))} invalid={!!err("startDate")} />
          </Field>
          <Field label={t("payroll.newPeriod.lastDay")} required error={err("endDate")} hint={len ? t("payroll.daysCount", { count: len }) : undefined} htmlFor="np-end">
            <DateInput id="np-end" value={range.end} min={range.start} onChange={(v) => setRange((r) => ({ ...r, end: v }))} invalid={!!err("endDate")} />
          </Field>
        </div>
      </form>
    </Modal>
  );
}
