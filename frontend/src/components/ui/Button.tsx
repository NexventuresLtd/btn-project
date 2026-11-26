import { forwardRef, useId, type ButtonHTMLAttributes, type MouseEvent, type ReactNode } from "react";
import { Loader2 } from "lucide-react";
import { toast } from "sonner";
import { cn } from "@/lib/cn";
import { useOnlineOnly, type OnlineOnlyKind } from "@/offline/onlineOnly";

const variants = {
  primary: "bg-navy-800 text-white hover:bg-navy-700 shadow-sm",
  accent: "bg-accent text-navy-950 hover:bg-amber-400 shadow-sm",
  secondary: "bg-white text-slate-700 border border-line hover:bg-slate-50 hover:border-slate-300",
  ghost: "text-slate-600 hover:bg-slate-100 hover:text-slate-900",
  danger: "bg-red-600 text-white hover:bg-red-700 shadow-sm",
  success: "bg-emerald-700 text-white hover:bg-emerald-800 shadow-sm", // white on emerald-600 is 3.7:1 (WCAG AA needs 4.5)
} as const;

const sizes = {
  sm: "min-h-8 py-1 px-3 text-xs gap-1.5 rounded-lg",
  md: "min-h-10 py-1.5 px-4 text-sm gap-2 rounded-xl",
  lg: "min-h-12 py-2 px-5 text-[15px] gap-2 rounded-xl",
  icon: "h-9 w-9 rounded-lg justify-center",
} as const;

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: keyof typeof variants;
  size?: keyof typeof sizes;
  loading?: boolean;
  icon?: ReactNode;
  /** SRS §5.2: approval / admin action (`true`) or server download (`"download"`) — unavailable offline, with the reason
   * as tooltip + description; the button stays focusable and a click only shows the reason. */
  onlineOnly?: boolean | OnlineOnlyKind;
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = "primary", size = "md", loading, icon, className, children, disabled, type = "button", onlineOnly, ...rest },
  ref,
) {
  const guard = useOnlineOnly(onlineOnly ? (onlineOnly === true ? "action" : onlineOnly) : false);
  const reasonId = useId();
  const blocked = guard.blocked && !disabled && !loading;
  return (
    <button
      ref={ref}
      type={type}
      disabled={disabled || loading}
      className={cn(
        "inline-flex items-center justify-center font-semibold text-center leading-tight transition-colors disabled:pointer-events-none disabled:opacity-55",
        variants[variant],
        sizes[size],
        blocked && "cursor-not-allowed opacity-55",
        className,
      )}
      {...rest}
      {...(blocked
        ? {
            "aria-disabled": true,
            "aria-describedby": cn(rest["aria-describedby"], reasonId),
            "data-online-only": "blocked",
            title: guard.reason,
            onClick: (e: MouseEvent<HTMLButtonElement>) => {
              e.preventDefault();
              e.stopPropagation();
              toast.info(guard.reason, { id: "online-only" });
            },
          }
        : {})}
    >
      {loading ? <Loader2 className="size-4 animate-spin" /> : icon}
      {children}
      {blocked ? <span id={reasonId} hidden>{guard.reason}</span> : null}
    </button>
  );
});
