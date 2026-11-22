import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { Check, Copy, MoreHorizontal, Search, X } from "lucide-react";
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

export interface MenuItem {
  label: string;
  icon?: ReactNode;
  onClick: () => void;
  tone?: "default" | "danger";
  hidden?: boolean;
}

/**
 * Row action menu. Rendered in a portal with fixed positioning so it is not
 * clipped by the table's horizontal scroll container.
 */
export function RowMenu({ items, label }: { items: MenuItem[]; label?: string }) {
  const { t } = useT();
  const [open, setOpen] = useState(false);
  const btn = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState<{ top: number; left: number } | null>(null);
  const visible = items.filter((i) => !i.hidden);

  useLayoutEffect(() => {
    if (!open || !btn.current) return;
    const place = () => {
      const r = btn.current!.getBoundingClientRect();
      const width = 224;
      const height = menu.current?.offsetHeight ?? visible.length * 40 + 8;
      const below = r.bottom + 6 + height <= window.innerHeight;
      setPos({ top: below ? r.bottom + 6 : Math.max(8, r.top - 6 - height), left: Math.max(8, Math.min(window.innerWidth - width - 8, r.right - width)) });
    };
    place();
    window.addEventListener("resize", place);
    window.addEventListener("scroll", place, true);
    return () => {
      window.removeEventListener("resize", place);
      window.removeEventListener("scroll", place, true);
    };
  }, [open, visible.length]);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (!menu.current?.contains(e.target as Node) && !btn.current?.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        setOpen(false);
        btn.current?.focus();
      }
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  useEffect(() => {
    if (open) menu.current?.querySelector<HTMLButtonElement>("button")?.focus();
  }, [open, pos]);

  if (!visible.length) return null;
  return (
    <>
      <button
        ref={btn}
        type="button"
        onClick={(e) => {
          e.stopPropagation();
          setOpen((o) => !o);
        }}
        className={cn("grid size-8 place-items-center rounded-lg text-slate-500 transition hover:bg-slate-100 hover:text-slate-900", open && "bg-slate-100 text-slate-900")}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={label ?? t("common.actions")}
      >
        <MoreHorizontal className="size-4" />
      </button>
      {open
        ? createPortal(
            <div
              ref={menu}
              role="menu"
              style={{ top: pos?.top ?? -9999, left: pos?.left ?? -9999 }}
              className="fixed z-50 w-56 rounded-xl border border-line bg-white p-1 shadow-xl shadow-navy-950/10"
              onKeyDown={(e) => {
                const buttons = [...(menu.current?.querySelectorAll<HTMLButtonElement>("button") ?? [])];
                const i = buttons.indexOf(document.activeElement as HTMLButtonElement);
                if (e.key === "ArrowDown") {
                  e.preventDefault();
                  buttons[(i + 1) % buttons.length]?.focus();
                } else if (e.key === "ArrowUp") {
                  e.preventDefault();
                  buttons[(i - 1 + buttons.length) % buttons.length]?.focus();
                }
              }}
            >
              {visible.map((item) => (
                <button
                  key={item.label}
                  type="button"
                  role="menuitem"
                  onClick={() => {
                    setOpen(false);
                    item.onClick();
                  }}
                  className={cn(
                    "flex w-full items-center gap-2.5 rounded-lg px-3 py-2 text-left text-sm font-medium transition outline-none [&>svg]:size-4",
                    item.tone === "danger" ? "text-red-600 hover:bg-red-50 focus:bg-red-50" : "text-slate-700 hover:bg-slate-100 focus:bg-slate-100",
                  )}
                >
                  {item.icon}
                  {item.label}
                </button>
              ))}
            </div>,
            document.body,
          )
        : null}
    </>
  );
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
