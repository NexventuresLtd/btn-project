import { Check, Circle } from "lucide-react";
import { cn } from "@/lib/cn";
import { tr, useT } from "@/i18n";

export const passwordChecks = (pw: string, t: (k: "auth.rules.length" | "auth.rules.upper" | "auth.rules.lower" | "auth.rules.number") => string = tr) => [
  { ok: pw.length >= 10, label: t("auth.rules.length") },
  { ok: /[A-Z]/.test(pw), label: t("auth.rules.upper") },
  { ok: /[a-z]/.test(pw), label: t("auth.rules.lower") },
  { ok: /[0-9]/.test(pw), label: t("auth.rules.number") },
];

export const passwordValid = (pw: string) => passwordChecks(pw).every((c) => c.ok);

export function PasswordRules({ password }: { password: string }) {
  const { t } = useT();
  return (
    <ul className="grid grid-cols-2 gap-x-3 gap-y-1.5">
      {passwordChecks(password, t).map((c) => (
        <li key={c.label} className={cn("flex items-center gap-1.5 text-xs", c.ok ? "text-emerald-700" : "text-slate-500")}>
          {c.ok ? <Check className="size-3.5" /> : <Circle className="size-3" />} {c.label}
        </li>
      ))}
    </ul>
  );
}
