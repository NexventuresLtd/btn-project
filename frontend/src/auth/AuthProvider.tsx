import { createContext, useContext, useEffect, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, get, post, setUnauthorizedHandler } from "@/lib/api";
import { isLang, setLang, type Lang } from "@/i18n";

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
  logout: () => Promise<void>;
}

const Ctx = createContext<AuthCtx | null>(null);
export const ME_KEY = ["auth", "me"] as const;

export function AuthProvider({ children }: { children: ReactNode }) {
  const qc = useQueryClient();
  const { data, isLoading } = useQuery({
    queryKey: ME_KEY,
    queryFn: async () => {
      try {
        return (await get<{ user: CurrentUser }>("/auth/me")).user;
      } catch (e) {
        if (e instanceof ApiError && e.status === 401) return null;
        throw e;
      }
    },
    staleTime: 60_000,
    retry: false,
  });

  useEffect(() => {
    // Session expired mid-use: drop cached data and send the user to sign in.
    setUnauthorizedHandler(() => {
      qc.setQueryData(ME_KEY, null);
    });
  }, [qc]);

  const user = data ?? null;
  const userLang = user?.language;
  // the saved per-user language wins after sign-in
  useEffect(() => {
    if (userLang && isLang(userLang)) setLang(userLang);
  }, [userLang, user?.id]);
  const value: AuthCtx = {
    user,
    loading: isLoading,
    can: (p) => !!user && (user.role === "SUPER_ADMIN" || user.permissions.includes(p)),
    setUser: (u) => qc.setQueryData(ME_KEY, u),
    logout: async () => {
      await post("/auth/logout").catch(() => {});
      qc.clear();
      qc.setQueryData(ME_KEY, null);
    },
  };
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useAuth() {
  const ctx = useContext(Ctx);
  if (!ctx) throw new Error("useAuth must be used inside <AuthProvider>");
  return ctx;
}
