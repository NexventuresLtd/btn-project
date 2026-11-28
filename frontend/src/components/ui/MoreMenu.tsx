import { useEffect, useId, useLayoutEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { useNavigate } from "react-router";
import { MoreHorizontal } from "lucide-react";
import { cn } from "@/lib/cn";
import { useT } from "@/i18n";

export interface MoreMenuItem {
  label: string;
  icon?: ReactNode;
  /** run on select … */
  onClick?: () => void;
  /** … or go to this route */
  to?: string;
  tone?: "default" | "danger";
  hidden?: boolean;
  disabled?: boolean;
}

const MENU_WIDTH = 240;

/**
 * "⋯" overflow menu for secondary actions (page headers and table rows).
 * Button with aria-haspopup="menu"; the menu is a portal with fixed positioning so a table's scroll container never
 * clips it. Keyboard: Enter/Space/↓ opens and focuses the first item, ↑/↓ move, Home/End jump, Esc closes and
 * returns focus to the button, Tab closes. A click outside closes it. Renders nothing when every item is hidden.
 */
export function MoreMenu({ items, label, size = "md", className }: {
  items: MoreMenuItem[];
  /** accessible name of the button (default "More actions") */
  label?: string;
  /** "md" matches secondary buttons in page headers; "row" is the compact table-row trigger */
  size?: "md" | "row";
  className?: string;
}) {
  const { t } = useT();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const btn = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState<{ top: number; left: number } | null>(null);
  const menuId = useId();
  const visible = items.filter((i) => !i.hidden);

  useLayoutEffect(() => {
    if (!open || !btn.current) return;
    const place = () => {
      const r = btn.current!.getBoundingClientRect();
      const width = Math.min(MENU_WIDTH, window.innerWidth - 16);
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
        e.preventDefault();
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

  const buttons = () => [...(menu.current?.querySelectorAll<HTMLButtonElement>("button[role=menuitem]:not(:disabled)") ?? [])];

  useEffect(() => {
    if (open && pos) buttons()[0]?.focus();
  }, [open, pos]);

  if (!visible.length) return null;

  const onMenuKey = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    const list = buttons();
    if (!list.length) return;
    const i = list.indexOf(document.activeElement as HTMLButtonElement);
    const go = (n: number) => {
      e.preventDefault();
      list[(n + list.length) % list.length]?.focus();
    };
    if (e.key === "ArrowDown") go(i + 1);
    else if (e.key === "ArrowUp") go(i < 0 ? list.length - 1 : i - 1);
    else if (e.key === "Home") go(0);
    else if (e.key === "End") go(list.length - 1);
    else if (e.key === "Tab") {
      setOpen(false);
      btn.current?.focus();
    }
  };

  const select = (item: MoreMenuItem) => {
    setOpen(false);
    btn.current?.focus();
    if (item.to) navigate(item.to);
    else item.onClick?.();
  };

  return (
    <>
      <button
        ref={btn}
        type="button"
        onClick={(e) => {
          e.stopPropagation();
          setOpen((o) => !o);
        }}
        onKeyDown={(e) => {
          e.stopPropagation(); // a focused table row opens on Enter: keep the key for the menu
          if (e.key === "ArrowDown" && !open) {
            e.preventDefault();
            setOpen(true);
          }
        }}
        className={cn(
          "grid shrink-0 place-items-center transition focus-visible:ring-2 focus-visible:ring-navy-300 focus-visible:outline-none",
          size === "md" ? "size-10 rounded-xl border border-line bg-white text-slate-700 hover:border-slate-300 hover:bg-slate-50" : "size-8 rounded-lg text-slate-500 hover:bg-slate-100 hover:text-slate-900",
          open && "bg-slate-100 text-slate-900",
          className,
        )}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        aria-label={label ?? t("common.moreActions")}
        title={label ?? t("common.moreActions")}
      >
        <MoreHorizontal className={size === "md" ? "size-5" : "size-4"} />
      </button>
      {open
        ? createPortal(
            <div
              ref={menu}
              id={menuId}
              role="menu"
              aria-label={label ?? t("common.moreActions")}
              style={{ top: pos?.top ?? -9999, left: pos?.left ?? -9999, width: `min(${MENU_WIDTH}px, calc(100vw - 16px))` }}
              className="fixed z-50 rounded-xl border border-line bg-white p-1 shadow-xl shadow-navy-950/10"
              onKeyDown={onMenuKey}
              onClick={(e) => e.stopPropagation()}
            >
              {visible.map((item) => (
                <button
                  key={item.label}
                  type="button"
                  role="menuitem"
                  tabIndex={-1}
                  disabled={item.disabled}
                  onClick={() => select(item)}
                  className={cn(
                    "flex min-h-10 w-full items-center gap-2.5 rounded-lg px-3 py-2 text-left text-sm font-medium transition outline-none disabled:opacity-50 [&>svg]:size-4 [&>svg]:shrink-0",
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
