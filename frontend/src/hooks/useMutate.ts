import { useMutation, useQueryClient, type QueryKey } from "@tanstack/react-query";
import { toast } from "sonner";
import { ApiError } from "@/lib/api";

/**
 * Mutation with the house conventions: toast on success/failure and
 * invalidate the given query keys. Field errors stay on error.fields for forms.
 */
export function useMutate<TVars, TData = unknown>(fn: (vars: TVars) => Promise<TData>, opts: { success?: string | ((d: TData) => string); invalidate?: QueryKey[]; onSuccess?: (d: TData, v: TVars) => void } = {}) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: (data, vars) => {
      for (const key of opts.invalidate ?? []) qc.invalidateQueries({ queryKey: key });
      if (opts.success) toast.success(typeof opts.success === "function" ? opts.success(data) : opts.success);
      opts.onSuccess?.(data, vars);
    },
    onError: (err) => {
      toast.error(err instanceof ApiError ? err.message : "Something went wrong");
    },
  });
}

/** Field-level message from a failed mutation, for <Field error=...> */
export function fieldError(err: unknown, name: string): string | undefined {
  return err instanceof ApiError ? err.fields?.[name] : undefined;
}
