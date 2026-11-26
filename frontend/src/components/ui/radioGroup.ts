import type { KeyboardEvent } from "react";

/**
 * Keyboard behaviour of a WAI-ARIA radio group built from `role="radio"` buttons (segmented controls):
 * one tab stop (the checked option, else the first), arrows move and select, Home/End jump to the ends.
 *
 *   const rg = radioGroupKeys(VALUES, value, onChange);
 *   <div role="radiogroup" onKeyDown={rg.onKeyDown}>
 *     {VALUES.map((v) => <button role="radio" aria-checked={v === value} tabIndex={rg.tabIndex(v)} data-value={v} … />)}
 */
export function radioGroupKeys<T extends string>(values: readonly T[], value: T | null | undefined, select: (v: T) => void) {
  const current = values.includes(value as T) ? (value as T) : values[0];
  return {
    tabIndex: (v: T) => (v === current ? 0 : -1),
    onKeyDown: (e: KeyboardEvent<HTMLElement>) => {
      const i = values.indexOf(current);
      const next = nextIndex(e.key, i, values.length);
      if (next === null) return;
      e.preventDefault();
      const v = values[next];
      select(v);
      const el = e.currentTarget.querySelector<HTMLElement>(`[role="radio"][data-value="${CSS.escape(v)}"]`);
      el?.focus();
    },
  };
}

/** Index after a key press in a list of n options (wrapping), or null when the key does not move. */
export function nextIndex(key: string, i: number, n: number): number | null {
  if (n === 0) return null;
  switch (key) {
    case "ArrowRight":
    case "ArrowDown":
      return (i + 1) % n;
    case "ArrowLeft":
    case "ArrowUp":
      return (i - 1 + n) % n;
    case "Home":
      return 0;
    case "End":
      return n - 1;
    default:
      return null;
  }
}
