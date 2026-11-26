import { createContext, useContext, useEffect, useSyncExternalStore, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, get, post, setUnauthorizedHandler } from "@/lib/api";
import { isLang, setLang, type Lang } from "@/i18n";
import { afterSignOut, confirmSignOut, getOfflineState, subscribeOffline } from "@/offline/runtime";
import { authState, forgetSharedMe, meRetry, meRetryDelay, sharedMe } from "./session";

// one /auth/me in flight per tab, shared across tabs, 429-safe (see ./session.ts)
const fetchMe = sharedMe(async () => {
  try {
    return (await get<{ user: CurrentUser }>("/auth/me")).user;
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) return null;
    throw e;
  }
});

export interface CurrentUser {
  id: string;
  email: string;
  name: string;
  role: string;
  roleLabel: string;
  plant: { id: string; code: string; name: string } | null;
  mustChangePassword: boolean;
  lastLoginAt: string | null;
  permissions: string[];
  language?: Lang;
  passkeyCount?: number;
}

interface AuthCtx {
  user: CurrentUser | null;
  loading: boolean;
  can: (permission: string) => boolean;
  setUser: (u: CurrentUser | null) => void;
  /** false when the sign-out guard was cancelled (unsynced offline records) */
  logout: () => Promise<boolean>;
  /** server unreachable: `user` (if any) comes from the device's offline package (F4) */
  offline: boolean;
}

const Ctx = createContext<AuthCtx | null>(null);
export const ME_KEY = ["auth", "me"] as const;

export function AuthProvider({ children }: { children: ReactNode }) {
  const qc = useQueryClient();
  const { data, isLoading, error } = useQuery({
    queryKey: ME_KEY,
    networkMode: "always", // offline: fail fast to the offline-unlock path instead of pausing
    queryFn: fetchMe,
    staleTime: 60_000,
    // rate limited / overloaded (429, 50x) is NOT signed out: retry with back-off, keep any known user
    retry: meRetry,
    retryDelay: meRetryDelay,
  });

  useEffect(() => {
    // Session expired mid-use: drop cached data and send the user to sign in.
    setUnauthorizedHandler(() => {
      qc.setQueryData(ME_KEY, null);
    });
  }, [qc]);

  // server unreachable → the user unlocked offline from the encrypted offline package (SRS OFF-12)
  const offlineUser = useSyncExternalStore(subscribeOffline, () => getOfflineState().offlineUser, () => null);
  const { user, offline, transient } = authState(data, error, offlineUser);
  // retries exhausted on 429/50x with no user yet: keep showing the loader and try again later
  useEffect(() => {
    if (!transient) return;
    const id = setTimeout(() => void qc.invalidateQueries({ queryKey: ME_KEY }), 30_000);
    return () => clearTimeout(id);
  }, [transient, error, qc]);
  const userLang = user?.language;
  // the saved per-user language wins after sign-in
  useEffect(() => {
    if (userLang && isLang(userLang)) setLang(userLang);
  }, [userLang, user?.id]);
  const value: AuthCtx = {
    user,
    loading: isLoading || transient,
    offline,
    can: (p) => !!user && (user.role === "SUPER_ADMIN" || user.permissions.includes(p)),
    setUser: (u) => qc.setQueryData(ME_KEY, u),
    logout: async () => {
      if (!(await confirmSignOut())) return false; // unsynced records: user cancelled the data-loss warning
      await post("/auth/logout").catch(() => {});
      await afterSignOut().catch(() => {}); // wipe this user's local data and keys (OFF-13)
      // signed-out first (the mounted observer sees it), then drop every other cached response
      forgetSharedMe();
      qc.setQueryData(ME_KEY, null);
      qc.removeQueries({ predicate: (q) => q.queryKey[0] !== ME_KEY[0] || q.queryKey[1] !== ME_KEY[1] });
      return true;
    },
  };
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useAuth() {
  const ctx = useContext(Ctx);
  if (!ctx) throw new Error("useAuth must be used inside <AuthProvider>");
  return ctx;
}
