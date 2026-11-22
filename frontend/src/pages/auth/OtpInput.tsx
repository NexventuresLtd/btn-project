import { useRef, type ClipboardEvent, type KeyboardEvent } from "react";
import { cn } from "@/lib/cn";
import { useT } from "@/i18n";

/** Six single-digit boxes with auto-advance, backspace and paste support. */
export function OtpInput({ value, onChange, invalid, disabled, onComplete }: { value: string; onChange: (v: string) => void; invalid?: boolean; disabled?: boolean; onComplete?: (v: string) => void }) {
  const { t } = useT();
  const refs = useRef<(HTMLInputElement | null)[]>([]);
  const digits = Array.from({ length: 6 }, (_, i) => value[i] ?? "");

  function setAt(i: number, d: string) {
    const next = (value.slice(0, i) + d + value.slice(i + 1)).replace(/\D/g, "").slice(0, 6);
    onChange(next);
    if (d && i < 5) refs.current[i + 1]?.focus();
    if (next.length === 6) onComplete?.(next);
  }
  function onKey(i: number, e: KeyboardEvent<HTMLInputElement>) {
    if (e.key === "Backspace" && !digits[i] && i > 0) {
      refs.current[i - 1]?.focus();
      onChange(value.slice(0, i - 1));
    }
    if (e.key === "ArrowLeft" && i > 0) refs.current[i - 1]?.focus();
    if (e.key === "ArrowRight" && i < 5) refs.current[i + 1]?.focus();
  }
  function onPaste(e: ClipboardEvent) {
    const text = e.clipboardData.getData("text").replace(/\D/g, "").slice(0, 6);
    if (!text) return;
    e.preventDefault();
    onChange(text);
    refs.current[Math.min(text.length, 5)]?.focus();
    if (text.length === 6) onComplete?.(text);
  }
  return (
    <div className="flex justify-between gap-2" onPaste={onPaste}>
      {digits.map((d, i) => (
        <input
          key={i}
          ref={(el) => { refs.current[i] = el; }}
          value={d}
          disabled={disabled}
          inputMode="numeric"
          autoComplete={i === 0 ? "one-time-code" : "off"}
          autoFocus={i === 0}
          maxLength={1}
          aria-label={t("auth.otp.digit", { n: i + 1 })}
          onChange={(e) => setAt(i, e.target.value.replace(/\D/g, "").slice(-1))}
          onKeyDown={(e) => onKey(i, e)}
          onFocus={(e) => e.target.select()}
          className={cn(
            "tabular h-14 w-full max-w-14 rounded-xl border bg-white text-center text-2xl font-bold text-navy-900 transition outline-none focus:ring-4",
            invalid ? "border-red-400 focus:ring-red-100" : "border-line focus:border-navy-500 focus:ring-navy-100",
            d && !invalid && "border-navy-300 bg-navy-50/40",
          )}
        />
      ))}
    </div>
  );
}
