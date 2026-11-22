import { useQuery } from "@tanstack/react-query";
import { del, get, patch } from "@/lib/api";

export interface Passkey {
  id: string;
  name: string;
  createdAt: string;
  lastUsedAt: string | null;
  backupEligible?: boolean;
  backupState?: boolean;
  transports?: string[];
  aaguid?: string;
}

export const PASSKEYS_KEY = ["account", "passkeys"] as const;

export function usePasskeys(enabled = true) {
  return useQuery({
    queryKey: PASSKEYS_KEY,
    enabled,
    queryFn: async ({ signal }) => {
      const r = await get<{ items?: Passkey[]; passkeys?: Passkey[] } | Passkey[]>("/account/passkeys", signal);
      return Array.isArray(r) ? r : (r.items ?? r.passkeys ?? []);
    },
  });
}

export const renamePasskey = (id: string, name: string) => patch(`/account/passkeys/${id}`, { name });
export const deletePasskey = (id: string) => del(`/account/passkeys/${id}`);
