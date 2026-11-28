import { useEffect, useState, type ReactNode } from "react";
import { Check, Copy, Search, X } from "lucide-react";
import { MoreMenu, type MoreMenuItem } from "@/components/ui/MoreMenu";
import { toast } from "sonner";
import { cn } from "@/lib/cn";
import { useT } from "@/i18n";

/** Debounced value, for search boxes that query the API. */
export function useDebounced<T>(value: T, ms = 300): T {
  const [v, setV] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setV(value), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return v;
}

export function SearchInput({ value, onChange, placeholder, className }: { value: string; onChange: (v: string) => void; placeholder?: string; className?: string }) {
  const { t } = useT();
  const ph = placeholder ?? t("common.searchPlaceholder");
  return (
    <div className={cn("relative", className)}>
      <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-slate-400" />
      <input
        type="search"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={ph}
        aria-label={ph}
        className="h-10 w-full rounded-xl border border-line bg-white pr-9 pl-9 text-sm text-slate-900 shadow-[0_1px_0_rgb(12_35_75/0.02)] placeholder:text-slate-400 focus:border-navy-500 focus:ring-4 focus:ring-navy-100 focus:outline-none [&::-webkit-search-cancel-button]:hidden"
      />
      {value ? (
        <button type="button" onClick={() => onChange("")} className="absolute top-1/2 right-2 -translate-y-1/2 rounded-md p-1 text-slate-400 hover:bg-slate-100 hover:text-slate-700" aria-label={t("admin.components.clearSearch")}>
          <X className="size-3.5" />
        </button>
      ) : null}
    </div>
  );
}

/** Row action menu: the shared "⋯" menu (components/ui/MoreMenu) with the compact row trigger. */
export type MenuItem = MoreMenuItem;
export function RowMenu({ items, label }: { items: MenuItem[]; label?: string }) {
  return <MoreMenu items={items} label={label} size="row" />;
}

/** A secret shown once (temporary password) with a copy button. */
export function CopyBox({ value, label }: { value: string; label?: string }) {
  const { t } = useT();
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      toast.error(t("admin.components.copyFailed"));
    }
  };
  return (
    <div>
      <p className="mb-1.5 text-[13px] font-medium text-slate-700">{label ?? t("admin.components.tempPassword")}</p>
      <div className="flex items-center gap-2 rounded-xl border border-navy-100 bg-navy-50/60 p-1.5 pl-3.5">
        <code className="min-w-0 flex-1 truncate font-mono text-[15px] font-semibold tracking-wide text-navy-900 select-all">{value}</code>
        <button type="button" onClick={copy} className="inline-flex h-9 shrink-0 items-center gap-1.5 rounded-lg bg-white px-3 text-xs font-semibold text-navy-800 shadow-sm ring-1 ring-line hover:bg-slate-50">
          {copied ? <Check className="size-4 text-emerald-600" /> : <Copy className="size-4" />}
          {copied ? t("admin.components.copied") : t("admin.components.copy")}
        </button>
      </div>
    </div>
  );
}

/** Greyed text for inactive master-data rows. */
export function Dim({ off, children, className }: { off: boolean; children: ReactNode; className?: string }) {
  return <span className={cn(off && "text-slate-400", className)}>{children}</span>;
}
