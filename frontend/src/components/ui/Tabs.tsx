import { cn } from "@/lib/cn";

export function Tabs<T extends string>({ tabs, value, onChange, className }: { tabs: { value: T; label: string; count?: number }[]; value: T; onChange: (v: T) => void; className?: string }) {
  return (
    <div className={cn("inline-flex rounded-xl bg-slate-100 p-1", className)} role="tablist">
      {tabs.map((t) => (
        <button
          key={t.value}
          role="tab"
          aria-selected={value === t.value}
          onClick={() => onChange(t.value)}
          className={cn(
            "rounded-lg px-3.5 py-1.5 text-[13px] font-semibold transition",
            value === t.value ? "bg-white text-navy-800 shadow-sm" : "text-slate-500 hover:text-slate-800",
          )}
        >
          {t.label}
          {t.count != null ? <span className="ml-1.5 rounded-md bg-slate-200/80 px-1.5 text-[11px] text-slate-600">{t.count}</span> : null}
        </button>
      ))}
    </div>
  );
}
