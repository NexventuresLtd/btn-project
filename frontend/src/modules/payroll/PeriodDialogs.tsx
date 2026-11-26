import { useEffect, useState } from "react";
import { toast } from "sonner";
import { TriangleAlert } from "lucide-react";
import { Alert, Button, Checkbox, DateInput, Field, Modal, Select, Textarea } from "@/components/ui";
import { useT } from "@/i18n";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { formatInt, formatRWF } from "@/lib/format";
import { todayISO } from "@/lib/dates";
import { PAYMENT_METHODS, PAYROLL_KEY, recordWagePayments, returnToDraft, type PaymentMethod, type PeriodDetail } from "./api";

const invalidate = (id: string) => [[...PAYROLL_KEY, "period", id], [...PAYROLL_KEY, "periods"]];

export function ReturnModal({ open, onClose, period }: { open: boolean; onClose: () => void; period: PeriodDetail }) {
  const { t, tx } = useT();
  const [comment, setComment] = useState("");
  const [touched, setTouched] = useState(false);
  useEffect(() => {
    if (open) {
      setComment("");
      setTouched(false);
    }
  }, [open]);
  const ret = useMutate(returnToDraft, { success: t("payroll.return.done", { number: period.number }), invalidate: invalidate(period.id), onSuccess: onClose });
  const error = touched && comment.trim().length < 3 ? t("payroll.return.error") : fieldError(ret.error, "comment");
  return (
    <Modal
      open={open}
      onClose={onClose}
      size="sm"
      title={t("payroll.return.title")}
      description={t("payroll.return.description")}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
          <Button onlineOnly
            variant="danger"
            loading={ret.isPending}
            onClick={() => {
              setTouched(true);
              if (comment.trim().length >= 3) ret.mutate({ id: period.id, comment: comment.trim() });
            }}
          >
            {t("payroll.return.title")}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        {period.trail.prepared ? (
          <p className="text-sm text-slate-600">{t("payroll.return.clears", { list: ["prepared", "checked", "verified"].filter((k) => period.trail[k as "prepared"]).map((k) => tx(`enums.status.${k.toUpperCase()}`)).join(", ") })}</p>
        ) : null}
        <Field label={t("payroll.return.label")} required error={error} htmlFor="ret-comment">
          <Textarea id="ret-comment" value={comment} onChange={(e) => setComment(e.target.value)} maxLength={500} invalid={!!error} placeholder={t("payroll.return.placeholder")} autoFocus />
        </Field>
      </div>
    </Modal>
  );
}

/** Record the wage payments of an authorised sheet: the money was paid outside the system; this keeps the record. */
export function RecordPaymentsModal({ open, onClose, period, canRecordPettyCash }: { open: boolean; onClose: () => void; period: PeriodDetail; canRecordPettyCash: boolean }) {
  const { t, tx } = useT();
  const payable = period.lines.filter((l) => l.netAmount > 0);
  const [refs, setRefs] = useState<Record<string, string>>({});
  const [lineMethods, setLineMethods] = useState<Record<string, PaymentMethod | "">>({});
  const [method, setMethod] = useState<PaymentMethod>("MOMO");
  const [paidOn, setPaidOn] = useState(todayISO());
  const [petty, setPetty] = useState(true);
  useEffect(() => {
    if (open) {
      setRefs({});
      setLineMethods({});
      setMethod("MOMO");
      setPaidOn(todayISO());
      setPetty(true);
    }
  }, [open]);

  const pay = useMutate(recordWagePayments, {
    success: (r) => (r.pettyCash.created ? t("payroll.pay.donePetty", { number: period.number, count: r.pettyCash.created }) : t("payroll.pay.done", { number: period.number })),
    invalidate: [...invalidate(period.id), ["pettycash"], ["expenses"], ["finance"], ["budgets"]],
    onSuccess: (r) => {
      if (r.period.payments?.count) toast.info(t("payroll.wrk.paymentsRecorded", { count: r.period.payments.count, amount: formatRWF(r.period.payments.amount) }));
      if (r.warning) toast.warning(r.warning, { duration: 12_000 });
      onClose();
    },
  });
  const mismatches = payable.filter((l) => l.worker.momoNameMismatch);
  const dateError = !paidOn ? t("payroll.wrk.err.paidOn") : paidOn > todayISO() ? t("payroll.wrk.err.paidFuture") : fieldError(pay.error, "paidOn");

  return (
    <Modal
      open={open}
      onClose={onClose}
      size="lg"
      title={t("payroll.pay.title")}
      description={t("payroll.pay.description", { workers: t("payroll.workers", { count: payable.length }), amount: formatRWF(period.totals.net) })}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
          <Button onlineOnly
            variant="success"
            loading={pay.isPending}
            disabled={!!dateError && !fieldError(pay.error, "paidOn")}
            onClick={() =>
              pay.mutate({
                id: period.id,
                method,
                paidOn,
                paymentRefs: payable
                  .filter((l) => (refs[l.id] ?? "").trim() || lineMethods[l.id])
                  .map((l) => ({ lineId: l.id, ref: (refs[l.id] ?? "").trim(), ...(lineMethods[l.id] ? { method: lineMethods[l.id] as PaymentMethod } : {}) })),
                createPettyCash: canRecordPettyCash && petty,
              })
            }
          >
            {t("payroll.pay.markPaid")}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <p className="text-sm text-slate-600">{t("payroll.pay.recordNote")}</p>
        {mismatches.length ? (
          <Alert tone="amber" title={t("payroll.pay.mismatchTitle", { count: mismatches.length })}>
            {mismatches.map((l) => `${l.worker.fullName} → ${l.worker.momoName}`).join(" · ")}
          </Alert>
        ) : null}
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t("payroll.wrk.method")} required htmlFor="pay-method" hint={t("payroll.wrk.methodHint")}>
            <Select id="pay-method" value={method} onChange={(e) => setMethod(e.target.value as PaymentMethod)}>
              {PAYMENT_METHODS.map((m) => <option key={m} value={m}>{tx(`payroll.wrk.method.${m}`)}</option>)}
            </Select>
          </Field>
          <Field label={t("payroll.wrk.paidOn")} required error={dateError} htmlFor="pay-date">
            <DateInput id="pay-date" value={paidOn} max={todayISO()} onChange={(v) => setPaidOn(v)} />
          </Field>
        </div>
        <div>
          <p className="mb-2 text-[13px] font-medium text-slate-700">{t("payroll.pay.refs")} <span className="font-normal text-slate-500">({t("common.optional")})</span></p>
          <div className="overflow-hidden rounded-xl border border-line">
            <table className="w-full text-sm">
              <thead>
                <tr className="bg-slate-50/70 text-[11px] font-semibold tracking-wide text-slate-500 uppercase">
                  <th className="px-3 py-2 text-left">{t("payroll.pay.colWorker")}</th>
                  <th className="hidden px-3 py-2 text-left sm:table-cell">{t("payroll.pay.colMomo")}</th>
                  <th className="px-3 py-2 text-right">{t("common.amount")}</th>
                  <th className="px-3 py-2 text-left">{t("payroll.wrk.method")}</th>
                  <th className="px-3 py-2 text-left">{t("payroll.pay.colReference")}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-line">
                {payable.map((l, i) => (
                  <tr key={l.id}>
                    <td className="px-3 py-2">
                      <p className="font-medium text-slate-900">{l.worker.fullName}</p>
                      {l.worker.momoNameMismatch ? (
                        <p className="flex items-center gap-1 text-xs font-semibold text-amber-700"><TriangleAlert className="size-3" /> {l.worker.momoName}</p>
                      ) : null}
                    </td>
                    <td className="tabular hidden px-3 py-2 text-xs text-slate-600 sm:table-cell">{l.worker.momoNumber ?? "—"}</td>
                    <td className="tabular px-3 py-2 text-right font-semibold">{formatInt(l.netAmount)}</td>
                    <td className="px-3 py-1.5">
                      <Select value={lineMethods[l.id] ?? ""} onChange={(e) => setLineMethods((m) => ({ ...m, [l.id]: e.target.value as PaymentMethod | "" }))} className="h-9 min-w-28" aria-label={t("payroll.wrk.methodAria", { name: l.worker.fullName })}>
                        <option value="">{t("payroll.wrk.methodDefault", { method: tx(`payroll.wrk.method.${method}`) })}</option>
                        {PAYMENT_METHODS.map((m) => <option key={m} value={m}>{tx(`payroll.wrk.method.${m}`)}</option>)}
                      </Select>
                    </td>
                    <td className="px-3 py-1.5">
                      <input
                        value={refs[l.id] ?? ""}
                        onChange={(e) => setRefs((r) => ({ ...r, [l.id]: e.target.value }))}
                        onKeyDown={(e) => {
                          if (e.key === "Enter") {
                            e.preventDefault();
                            document.querySelector<HTMLInputElement>(`[data-pay-row="${i + 1}"]`)?.focus();
                          }
                        }}
                        data-pay-row={i}
                        maxLength={60}
                        aria-label={t("payroll.pay.refAria", { name: l.worker.fullName })}
                        className="h-9 w-full min-w-36 rounded-lg border border-line px-2.5 text-sm focus:border-navy-500 focus:ring-2 focus:ring-navy-100 focus:outline-none"
                      />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
        {canRecordPettyCash ? (
          <div className="rounded-xl border border-line bg-slate-50/60 p-3.5">
            <Checkbox label={t("payroll.pay.pettyLabel")} checked={petty} onChange={(e) => setPetty(e.target.checked)} />
            <p className="mt-1 ml-6 text-xs text-slate-500">{petty ? t("payroll.pay.pettyNote", { amount: formatRWF(period.totals.net), number: period.number }) : t("payroll.pay.expenseNote", { number: period.number })}</p>
          </div>
        ) : <p className="text-xs text-slate-500">{t("payroll.pay.expenseNote", { number: period.number })}</p>}
      </div>
    </Modal>
  );
}
