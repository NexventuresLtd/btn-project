import { useEffect, useId, useRef, useState, type KeyboardEvent } from "react";
import { CalendarDays, ChevronLeft, ChevronRight } from "lucide-react";
import { useT } from "@/i18n";
import { addMonthsISO, calendarKeyTarget, dtf, formatDate, isoToDMY, monthGrid, parseDMY, shiftISO, todayISO } from "@/lib/dates";
import { cn } from "@/lib/cn";
import { useFieldAria } from "./Field";

export interface DateInputProps {
  /** ISO business date "YYYY-MM-DD" ("" = empty). The wire format stays ISO; only the display is DD/MM/YYYY. */
  value: string;
  /** Called with a valid ISO date inside [min, max] (or "" when the field is cleared and `clearable`). */
  onChange: (iso: string) => void;
  min?: string;
  max?: string;
  id?: string;
  invalid?: boolean;
  disabled?: boolean;
  required?: boolean;
  clearable?: boolean;
  /** "field" = full-width form control (default); "bare" = borderless, for inline filter bars. */
  variant?: "field" | "bare";
  className?: string;
  "aria-label"?: string;
  "aria-describedby"?: string;
}

const clamp = (iso: string, min?: string, max?: string) => (min && iso < min ? min : max && iso > max ? max : iso);

/**
 * Accessible DD/MM/YYYY date field (NFR-L1) with a calendar popover.
 * Typing: "14/11/2026", "14-11-2026", "14112026" or "14/11/26"; committed on blur / Enter.
 * Keyboard in the field: Alt+↓ or F4 opens the calendar, ↑/↓ move one day, PageUp/PageDown one month.
 * In the calendar (role=grid): arrows ±1 day / ±1 week, PageUp/PageDown month, Shift+PageUp/Down year,
 * Home/End week start/end, Enter/Space select, Escape closes and returns focus.
 */
export function DateInput({ value, onChange, min, max, id, invalid, disabled, required, clearable, variant = "field", className, ...aria }: DateInputProps) {
  const { t } = useT();
  const autoId = useId();
  const inputId = id ?? `date-${autoId}`;
  const field = useFieldAria({ id, required, "aria-label": aria["aria-label"], "aria-describedby": aria["aria-describedby"] }); // label/hint of the enclosing <Field>
  const [text, setText] = useState(isoToDMY(value));
  const [error, setError] = useState<string | null>(null);
  const [open, setOpen] = useState(false);
  const [focusDay, setFocusDay] = useState(value || clamp(todayISO(), min, max));
  const wrap = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const grid = useRef<HTMLDivElement>(null);

  useEffect(() => {
    setText(isoToDMY(value));
    setError(null);
  }, [value]);

  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent) => wrap.current && !wrap.current.contains(e.target as Node) && setOpen(false);
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, [open]);

  useEffect(() => {
    if (open) grid.current?.querySelector<HTMLButtonElement>(`[data-day="${focusDay}"]`)?.focus();
  }, [open, focusDay]);

  function validate(iso: string): string | null {
    if (min && iso < min) return t("datepicker.tooEarly", { date: formatDate(min) });
    if (max && iso > max) return t("datepicker.tooLate", { date: formatDate(max) });
    return null;
  }

  function commit(raw: string) {
    if (!raw.trim()) {
      if (clearable) {
        setError(null);
        if (value) onChange("");
      } else setText(isoToDMY(value));
      return;
    }
    const iso = parseDMY(raw);
    if (!iso) {
      setError(t("datepicker.invalid"));
      return;
    }
    const msg = validate(iso);
    if (msg) {
      setError(msg);
      return;
    }
    setError(null);
    setText(isoToDMY(iso));
    if (iso !== value) onChange(iso);
  }

  function pick(iso: string) {
    if (validate(iso)) return;
    setOpen(false);
    setError(null);
    setText(isoToDMY(iso));
    if (iso !== value) onChange(iso);
    input.current?.focus();
  }

  function openCalendar() {
    if (disabled) return;
    setFocusDay(clamp(parseDMY(text) ?? (value || todayISO()), min, max));
    setOpen(true);
  }

  function step(days: number, months = 0) {
    const base = parseDMY(text) ?? (value || todayISO());
    const next = clamp(months ? addMonthsISO(base, months) : shiftISO(base, days), min, max);
    setText(isoToDMY(next));
    setError(null);
    if (next !== value) onChange(next);
  }

  function onFieldKey(e: KeyboardEvent<HTMLInputElement>) {
    if ((e.altKey && e.key === "ArrowDown") || e.key === "F4") {
      e.preventDefault();
      openCalendar();
    } else if (e.key === "Enter") {
      commit(text);
    } else if (e.key === "ArrowUp" || e.key === "ArrowDown") {
      e.preventDefault();
      step(e.key === "ArrowUp" ? 1 : -1);
    } else if (e.key === "PageUp" || e.key === "PageDown") {
      e.preventDefault();
      step(0, e.key === "PageUp" ? 1 : -1);
    }
  }

  function onGridKey(e: KeyboardEvent<HTMLDivElement>) {
    const target = calendarKeyTarget(e.key, focusDay, e.shiftKey);
    if (target) {
      e.preventDefault();
      setFocusDay(target);
    } else if (e.key === "Escape") {
      e.preventDefault();
      setOpen(false);
      input.current?.focus();
    } else if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      pick(focusDay);
    }
  }

  const days = monthGrid(focusDay);
  const month = focusDay.slice(0, 7);
  const today = todayISO();
  const monthLabel = dtf({ timeZone: "UTC", month: "long", year: "numeric" }).format(new Date(`${month}-01T00:00:00Z`));
  const weekdayFmt = dtf({ timeZone: "UTC", weekday: "short" });
  const longFmt = dtf({ timeZone: "UTC", weekday: "long", day: "numeric", month: "long", year: "numeric" });
  const shownInvalid = invalid || !!error;
  const errId = `${inputId}-err`;

  return (
    <div ref={wrap} className={cn("relative", variant === "field" ? "w-full" : "inline-flex", className)}>
      <div
        className={cn(
          "flex items-center",
          variant === "field" &&
            "h-10 w-full rounded-xl border border-line bg-white pr-1 shadow-[0_1px_0_rgb(12_35_75/0.02)] transition focus-within:border-navy-500 focus-within:ring-4 focus-within:ring-navy-100",
          variant === "field" && shownInvalid && "border-red-400 focus-within:ring-red-100",
          disabled && "bg-slate-50",
        )}
      >
        <input
          ref={input}
          id={inputId}
          type="text"
          inputMode="numeric"
          autoComplete="off"
          placeholder={t("datepicker.placeholder")}
          value={text}
          disabled={disabled}
          required={required}
          aria-invalid={shownInvalid || undefined}
          aria-describedby={[error ? errId : null, field["aria-describedby"]].filter(Boolean).join(" ") || undefined}
          aria-label={aria["aria-label"]}
          aria-labelledby={field["aria-labelledby"]}
          aria-required={field["aria-required"]}
          onChange={(e) => {
            setText(e.target.value);
            if (error) setError(null);
          }}
          onBlur={() => commit(text)}
          onKeyDown={onFieldKey}
          className={cn(
            "min-w-0 bg-transparent text-sm text-slate-900 tabular-nums placeholder:text-slate-400 focus:outline-none disabled:text-slate-500",
            variant === "field" ? "h-full flex-1 px-3.5" : "h-9 w-[6.6rem] px-1 text-slate-700",
          )}
        />
        <button
          type="button"
          onClick={() => (open ? setOpen(false) : openCalendar())}
          disabled={disabled}
          aria-label={t("datepicker.open")}
          aria-haspopup="dialog"
          aria-expanded={open}
          aria-controls={open ? `${inputId}-cal` : undefined}
          className="grid size-8 shrink-0 place-items-center rounded-lg text-slate-400 transition hover:bg-slate-100 hover:text-navy-700 focus-visible:ring-2 focus-visible:ring-navy-300 focus-visible:outline-none disabled:opacity-50"
        >
          <CalendarDays className="size-4" />
        </button>
      </div>
      {error ? (
        <p id={errId} role="alert" className={cn("mt-1 text-xs font-medium text-red-600", variant === "bare" && "absolute top-full left-0 z-30 w-56 rounded-lg bg-white p-1.5 shadow")}>
          {error}
        </p>
      ) : null}
      {open ? (
        <div id={`${inputId}-cal`} role="dialog" aria-modal="false" aria-label={t("datepicker.calendar")} className="absolute top-full left-0 z-50 mt-2 w-[17.5rem] rounded-2xl border border-line bg-white p-3 shadow-xl">
          <div className="mb-2 flex items-center justify-between">
            <button type="button" onClick={() => setFocusDay(addMonthsISO(focusDay, -1))} aria-label={t("datepicker.prevMonth")} className="grid size-8 place-items-center rounded-lg text-slate-600 hover:bg-slate-100">
              <ChevronLeft className="size-4" />
            </button>
            <p className="text-sm font-semibold text-slate-800 capitalize" aria-live="polite">{monthLabel}</p>
            <button type="button" onClick={() => setFocusDay(addMonthsISO(focusDay, 1))} aria-label={t("datepicker.nextMonth")} className="grid size-8 place-items-center rounded-lg text-slate-600 hover:bg-slate-100">
              <ChevronRight className="size-4" />
            </button>
          </div>
          <div ref={grid} role="grid" aria-label={monthLabel} onKeyDown={onGridKey}>
            <div role="row" className="grid grid-cols-7">
              {days.slice(0, 7).map((d) => (
                <span key={d} role="columnheader" className="py-1 text-center text-[11px] font-semibold text-slate-500 uppercase">
                  {weekdayFmt.format(new Date(`${d}T00:00:00Z`)).slice(0, 3)}
                </span>
              ))}
            </div>
            {[0, 1, 2, 3, 4, 5].map((w) => (
              <div role="row" key={w} className="grid grid-cols-7">
                {days.slice(w * 7, w * 7 + 7).map((d) => {
                  const out = !!validate(d);
                  const selected = d === value;
                  return (
                    <span role="gridcell" key={d} aria-selected={selected}>
                      <button
                        type="button"
                        data-day={d}
                        tabIndex={d === focusDay ? 0 : -1}
                        disabled={out}
                        aria-label={longFmt.format(new Date(`${d}T00:00:00Z`))}
                        aria-current={d === today ? "date" : undefined}
                        onClick={() => pick(d)}
                        className={cn(
                          "mx-auto grid size-9 place-items-center rounded-lg text-sm tabular-nums transition focus-visible:ring-2 focus-visible:ring-navy-400 focus-visible:outline-none",
                          d.slice(0, 7) !== month ? "text-slate-400" : "text-slate-800",
                          d === today && !selected && "font-bold text-navy-700 ring-1 ring-navy-200",
                          selected ? "bg-navy-800 font-semibold text-white" : "hover:bg-slate-100",
                          out && "cursor-not-allowed text-slate-300 line-through hover:bg-transparent",
                        )}
                      >
                        {Number(d.slice(8, 10))}
                      </button>
                    </span>
                  );
                })}
              </div>
            ))}
          </div>
          <div className="mt-2 flex justify-between border-t border-line pt-2">
            <button type="button" disabled={!!validate(today)} onClick={() => pick(today)} className="rounded-lg px-2 py-1 text-xs font-semibold text-navy-700 hover:bg-navy-50 disabled:opacity-40">
              {t("datepicker.today")}
            </button>
            <span className="self-center text-xs text-slate-500 tabular-nums">{value ? formatDate(value) : t("datepicker.placeholder")}</span>
          </div>
        </div>
      ) : null}
    </div>
  );
}
