import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "react-router";
import { Toaster } from "sonner";
import { AuthProvider } from "@/auth/AuthProvider";
import { OfflineProvider } from "@/offline/OfflineProvider";
import { router } from "@/app/router";
import { ApiError } from "@/lib/api";
import { initBranding } from "@/branding";
import { useRemoteOverrides } from "@/i18n/remote";
import "./index.css";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 60_000,
      gcTime: 10 * 60_000,
      refetchOnWindowFocus: false,
      retry: (count, err) => !(err instanceof ApiError && err.status < 500) && count < 2,
    },
    // SRS §5.2: a direct mutation (approval, admin change…) is never paused offline and replayed later — it runs and
    // lib/api refuses it with an explanation. Offline capture goes through the encrypted outbox instead.
    mutations: { networkMode: "always", retry: false },
  },
});

// Cached branding is applied synchronously (no flash); fresh branding follows from the API.
void initBranding();

function Bootstrap() {
  useRemoteOverrides();
  return null;
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <AuthProvider>
        <Bootstrap />
        <OfflineProvider>
          <RouterProvider router={router} />
        </OfflineProvider>
        <Toaster position="top-right" richColors closeButton toastOptions={{ style: { fontFamily: "inherit" } }} />
      </AuthProvider>
    </QueryClientProvider>
  </StrictMode>,
);
