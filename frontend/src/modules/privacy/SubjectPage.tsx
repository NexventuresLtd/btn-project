import { useState } from "react";
import { Link, useParams } from "react-router";
import { ArrowLeft, ArrowRight, Download, FileText, History, PencilLine } from "lucide-react";
import { Button, Card, CardBody, CardHeader, ErrorState, PageHeader, PageLoader, Spinner } from "@/components/ui";
import { useT } from "@/i18n";
import { downloadPdf, openPdf } from "@/lib/api";
import { formatDateTime } from "@/lib/dates";
import { formatInt, formatRWF } from "@/lib/format";
import { exportPath, pdfPath, useCorrections, useSubject, type SubjectDetail, type SubjectType } from "./api";
import { AccessLogTable, CorrectModal, fieldLabel, showValue } from "./components";

const ORDER: Record<SubjectType, string[]> = {
  worker: ["firstName", "lastName", "nationalId", "phone", "momoNumber", "momoName", "jobTitle", "plant", "dailyWage", "startDate", "isActive", "createdAt", "updatedAt"],
  customer: ["name", "tin", "phone", "email", "address", "isActive", "createdAt"],
};
const HIDDEN = new Set(["id", "plantId", "server_xid", "server_version"]); // sync bookkeeping (still in the JSON export)

export function SubjectPage() {
  const { type: rawType = "", id = "" } = useParams();
  const type: SubjectType = rawType === "customer" ? "customer" : "worker";
  const { t } = useT();
  const subject = useSubject(type, id);

  return (
    <>
      <PageHeader
        title={subject.data?.name ?? t("privacy.title")}
        description={t(`privacy.type.${type}`)}
        breadcrumbs={[{ label: t("privacy.breadcrumb") }, { label: t("privacy.nav"), to: "/admin/privacy" }, { label: subject.data?.name ?? "…" }]}
        actions={subject.data ? <Actions subject={subject.data} /> : null}
      />
      <Link to="/admin/privacy" className="mb-4 inline-flex items-center gap-1.5 text-sm font-medium text-navy-700 hover:underline">
        <ArrowLeft className="size-4" aria-hidden />
        {t("privacy.subject.back")}
      </Link>
      {subject.isError ? (
        <Card><ErrorState error={subject.error} retry={() => subject.refetch()} /></Card>
      ) : subject.isLoading || !subject.data ? (
        <PageLoader />
      ) : (
        <Detail subject={subject.data} />
      )}
    </>
  );
}

function Actions({ subject }: { subject: SubjectDetail }) {
  const { t } = useT();
  const [correcting, setCorrecting] = useState(false);
  return (
    <>
      <Button variant="secondary" icon={<Download className="size-4" />} onClick={() => downloadPdf(exportPath(subject.type, subject.id))}>{t("privacy.subject.download")}</Button>
      <Button variant="secondary" icon={<FileText className="size-4" />} onClick={() => openPdf(pdfPath(subject.type, subject.id))}>{t("privacy.subject.pdf")}</Button>
      <Button icon={<PencilLine className="size-4" />} onClick={() => setCorrecting(true)}>{t("privacy.subject.correct")}</Button>
      <CorrectModal key={JSON.stringify(subject.profile)} subject={subject} open={correcting} onClose={() => setCorrecting(false)} />
    </>
  );
}

function Detail({ subject }: { subject: SubjectDetail }) {
  const { t, tx } = useT();
  const keys = [...ORDER[subject.type].filter((k) => k in subject.profile), ...Object.keys(subject.profile).filter((k) => !ORDER[subject.type].includes(k) && !HIDDEN.has(k)).sort()];

  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-5">
      <Card className="min-w-0 lg:col-span-2">
        <CardHeader title={t("privacy.subject.profile")} />
        <CardBody>
          <dl className="divide-y divide-line text-sm">
            {keys.map((k) => (
              <div key={k} className="grid grid-cols-5 gap-3 py-2">
                <dt className="col-span-2 text-slate-600">{fieldLabel(k, tx)}</dt>
                <dd className="col-span-3 font-medium break-words text-slate-900">{showValue(k, subject.profile[k], t)}</dd>
              </div>
            ))}
            <div className="grid grid-cols-5 gap-3 py-2">
              <dt className="col-span-2 text-slate-600">{t("privacy.subject.recordId")}</dt>
              <dd className="col-span-3 font-mono text-xs break-all text-slate-700">{subject.id}</dd>
            </div>
          </dl>
        </CardBody>
      </Card>

      <div className="min-w-0 space-y-4 lg:col-span-3">
        <Card>
          <CardHeader title={t("privacy.subject.held")} description={t("privacy.subject.heldHint")} />
          <CardBody>
            <ul className="grid grid-cols-2 gap-3 sm:grid-cols-3">
              {subject.categories.map((c) => (
                <li key={c.key} className="rounded-xl border border-line bg-slate-50/60 px-4 py-3">
                  <p className="text-2xl font-semibold text-slate-900 tabular">{formatInt(c.count)}</p>
                  <p className="mt-0.5 text-[13px] text-slate-600">{tx(`privacy.cat.${c.key}`, undefined, c.key)}</p>
                </li>
              ))}
            </ul>
            {subject.totals ? (
              <div className="mt-4">
                <h4 className="mb-2 text-xs font-semibold tracking-wide text-slate-600 uppercase">{t("privacy.subject.totals")}</h4>
                <dl className="grid grid-cols-1 gap-3 sm:grid-cols-3">
                  {(["invoiced", "paid", "balance"] as const).map((k) => (
                    <div key={k} className="rounded-xl border border-line px-4 py-3">
                      <dt className="text-[13px] text-slate-600">{t(`privacy.kpi.${k}`)}</dt>
                      <dd className="mt-0.5 font-semibold text-slate-900 tabular">{formatRWF(subject.totals![k])}</dd>
                    </div>
                  ))}
                </dl>
              </div>
            ) : null}
          </CardBody>
        </Card>
        <Corrections subject={subject} />
      </div>

      <Card className="min-w-0 lg:col-span-5">
        <CardHeader title={t("privacy.accessLog.title")} />
        <AccessLogTable subjectType={subject.type} subjectId={subject.id} />
      </Card>
    </div>
  );
}

function Corrections({ subject }: { subject: SubjectDetail }) {
  const { t, tx } = useT();
  const list = useCorrections(subject.type, subject.id);
  return (
    <Card>
      <CardHeader title={t("privacy.corrections.title")} />
      {list.isError ? (
        <ErrorState error={list.error} retry={() => list.refetch()} />
      ) : list.isLoading ? (
        <div className="py-10"><Spinner className="mx-auto" /></div>
      ) : !list.data?.items.length ? (
        <CardBody>
          <p className="flex items-center gap-2 text-sm text-slate-600"><History className="size-4" aria-hidden />{t("privacy.corrections.empty")}</p>
        </CardBody>
      ) : (
        <ol className="divide-y divide-line border-t border-line">
          {list.data.items.map((c) => (
            <li key={c.id} className="px-5 py-3.5">
              <p className="text-xs text-slate-600">
                <span className="tabular">{formatDateTime(c.createdAt)}</span>
                {c.user ? <> · {t("privacy.corrections.by", { name: c.user.name })}</> : null}
                {c.requestRef ? <> · {t("privacy.corrections.ref", { ref: c.requestRef })}</> : null}
              </p>
              <p className="mt-1 text-sm text-slate-800">{c.reason}</p>
              <ul className="mt-2 space-y-1 text-[13px]">
                {Object.entries(c.changes).sort(([a], [b]) => a.localeCompare(b)).map(([f, ch]) => (
                  <li key={f} className="flex flex-wrap items-center gap-x-2">
                    <span className="w-36 shrink-0 font-medium text-slate-700">{fieldLabel(f, tx)}</span>
                    <span className="text-red-700 line-through decoration-red-300">{ch.before ?? t("privacy.correct.empty")}</span>
                    <ArrowRight className="size-3.5 text-slate-400" aria-hidden />
                    <span className="font-medium text-emerald-800">{ch.after ?? t("privacy.correct.empty")}</span>
                  </li>
                ))}
              </ul>
            </li>
          ))}
        </ol>
      )}
    </Card>
  );
}
