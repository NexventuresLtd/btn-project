import { useEffect, useRef, useState, type ReactNode } from "react";
import { cn } from "@/lib/cn";

/**
 * Horizontally scrollable wrapper for wide tables. While its content overflows it is a keyboard tab stop
 * (WCAG 2.1.1, axe "scrollable-region-focusable"), so keyboard users can scroll it with the arrow keys;
 * when everything fits it adds no extra tab stop.
 */
export function ScrollX({ children, className, label }: { children: ReactNode; className?: string; label?: string }) {
  const ref = useRef<HTMLDivElement>(null);
  const [overflows, setOverflows] = useState(false);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const check = () => setOverflows(el.scrollWidth > el.clientWidth + 1);
    check();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(check);
    ro.observe(el);
    if (el.firstElementChild) ro.observe(el.firstElementChild);
    return () => ro.disconnect();
  }, []);
  return (
    <div
      ref={ref}
      className={cn("overflow-x-auto", overflows && "focus-visible:ring-2 focus-visible:ring-navy-500 focus-visible:outline-none", className)}
      tabIndex={overflows ? 0 : undefined}
      role={overflows && label ? "region" : undefined}
      aria-label={overflows && label ? label : undefined}
    >
      {children}
    </div>
  );
}
