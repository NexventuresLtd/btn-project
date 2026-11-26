import { createContext, forwardRef, useContext, useId, type AriaAttributes, type InputHTMLAttributes, type ReactNode, type SelectHTMLAttributes, type TextareaHTMLAttributes } from "react";
import { cn } from "@/lib/cn";

const control =
  "w-full rounded-xl border border-line bg-white px-3.5 text-sm text-slate-900 placeholder:text-slate-400 shadow-[0_1px_0_rgb(12_35_75/0.02)] transition focus:border-navy-500 focus:ring-4 focus:ring-navy-100 focus:outline-none disabled:bg-slate-50 disabled:text-slate-500 aria-[invalid=true]:border-red-400 aria-[invalid=true]:focus:ring-red-100";

/**
 * What a <Field> tells the controls inside it (WCAG 1.3.1 / 4.1.2): the label's id, the error/hint id and whether
 * it is required. A control without its own name (aria-label, aria-labelledby, or an id the label points at
 * via htmlFor) is labelled by the Field's label, and its error/hint is announced as its description.
 */
interface FieldInfo {
  labelId?: string;
  htmlFor?: string;
  messageId?: string;
  required?: boolean;
}
const FieldContext = createContext<FieldInfo | null>(null);

type Named = { id?: string; required?: boolean } & Pick<AriaAttributes, "aria-label" | "aria-labelledby" | "aria-describedby" | "aria-required">;

/** ARIA props linking a control to its enclosing <Field> (used by Input, Select, Textarea and DateInput). */
export function useFieldAria(p: Named): Pick<AriaAttributes, "aria-labelledby" | "aria-describedby" | "aria-required"> {
  const f = useContext(FieldContext);
  if (!f) return { "aria-labelledby": p["aria-labelledby"], "aria-describedby": p["aria-describedby"], "aria-required": p["aria-required"] };
  const named = !!p["aria-label"] || !!p["aria-labelledby"] || (!!p.id && p.id === f.htmlFor);
  const described = [p["aria-describedby"], f.messageId].filter(Boolean).join(" ");
  return {
    "aria-labelledby": named ? p["aria-labelledby"] : f.labelId,
    "aria-describedby": described || undefined,
    "aria-required": p["aria-required"] ?? (p.required || f.required || undefined),
  };
}

export const Input = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement> & { invalid?: boolean }>(function Input(
  { className, invalid, ...rest },
  ref,
) {
  const aria = useFieldAria(rest);
  return <input ref={ref} aria-invalid={invalid || undefined} className={cn(control, "h-10", className)} {...rest} {...aria} />;
});

export const Textarea = forwardRef<HTMLTextAreaElement, TextareaHTMLAttributes<HTMLTextAreaElement> & { invalid?: boolean }>(function Textarea(
  { className, invalid, rows = 3, ...rest },
  ref,
) {
  const aria = useFieldAria(rest);
  return <textarea ref={ref} rows={rows} aria-invalid={invalid || undefined} className={cn(control, "py-2.5", className)} {...rest} {...aria} />;
});

export const Select = forwardRef<HTMLSelectElement, SelectHTMLAttributes<HTMLSelectElement> & { invalid?: boolean }>(function Select(
  { className, invalid, children, ...rest },
  ref,
) {
  const aria = useFieldAria(rest);
  return (
    <select ref={ref} {...aria} aria-invalid={invalid || undefined} className={cn(control, "h-10 pr-9 appearance-none bg-[url('data:image/svg+xml;utf8,<svg xmlns=%22http://www.w3.org/2000/svg%22 width=%2216%22 height=%2216%22 fill=%22none%22 stroke=%22%2364748b%22 stroke-width=%222%22 viewBox=%220 0 24 24%22><path d=%22m6 9 6 6 6-6%22/></svg>')] bg-[length:16px] bg-[right_0.75rem_center] bg-no-repeat", className)} {...rest} {...aria}>
      {children}
    </select>
  );
});

export function Field({ label, error, hint, required, children, className, htmlFor }: { label?: ReactNode; error?: string; hint?: ReactNode; required?: boolean; children: ReactNode; className?: string; htmlFor?: string }) {
  const uid = useId();
  const info: FieldInfo = { labelId: label ? `${uid}-label` : undefined, htmlFor, messageId: error || hint ? `${uid}-msg` : undefined, required };
  return (
    <div className={cn("space-y-1.5", className)}>
      {label ? (
        <label id={info.labelId} htmlFor={htmlFor} className="block text-[13px] font-medium text-slate-700">
          {label}
          {required ? <span className="ml-0.5 text-red-500" aria-hidden="true">*</span> : null}
        </label>
      ) : null}
      <FieldContext.Provider value={info}>{children}</FieldContext.Provider>
      {error ? <p id={info.messageId} className="text-xs font-medium text-red-600">{error}</p> : hint ? <p id={info.messageId} className="text-xs text-slate-500">{hint}</p> : null}
    </div>
  );
}

export function Checkbox({ label, className, ...rest }: InputHTMLAttributes<HTMLInputElement> & { label: ReactNode }) {
  return (
    <label className={cn("inline-flex cursor-pointer items-center gap-2 text-sm text-slate-700 select-none", className)}>
      <input type="checkbox" className="size-4 rounded border-slate-300 accent-navy-800" {...rest} />
      {label}
    </label>
  );
}
