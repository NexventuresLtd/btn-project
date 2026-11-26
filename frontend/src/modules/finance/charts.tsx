import { useLayoutEffect, useRef, useState, type ReactNode } from "react";

function useWidth<T extends HTMLElement>() {
  const ref = useRef<T>(null);
  const [width, setWidth] = useState(0);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    setWidth(el.clientWidth);
    const ro = new ResizeObserver(() => setWidth(el.clientWidth));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return [ref, width] as const;
}

/** Renders a Recharts chart at the measured width of its container (no horizontal overflow on phones). */
export function Measured({ height, children }: { height: number; children: (w: number, h: number) => ReactNode }) {
  const [ref, width] = useWidth<HTMLDivElement>();
  return <div ref={ref} className="w-full min-w-0" style={{ height }}>{width > 0 ? children(width, height) : null}</div>;
}

