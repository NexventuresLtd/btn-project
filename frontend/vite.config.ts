import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import path from "node:path";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: { alias: { "@": path.resolve(__dirname, "src") } },
  server: {
    port: 5173,
    // Same-origin in development: the session cookie and CSRF origin check just work.
    proxy: { "/api": { target: process.env.API_TARGET ?? "http://localhost:4000", changeOrigin: false } },
    host: true,
    watch: { usePolling: false },
  },
  build: {
    sourcemap: true,
    chunkSizeWarningLimit: 900,
    rollupOptions: {
      output: {
        manualChunks(id: string) {
          if (!id.includes("node_modules")) return undefined;
          if (/node_modules\/(recharts|d3-|victory-vendor|decimal\.js-light|internmap|eventemitter3|reduce-css-calc|es-toolkit|immer|redux|react-redux|@reduxjs)/.test(id)) return "charts";
          if (/node_modules\/@tanstack/.test(id)) return "query";
          if (/node_modules\/(react-router|@remix-run|cookie|set-cookie-parser|turbo-stream)/.test(id)) return "router";
          if (/node_modules\/(react|react-dom|scheduler)\//.test(id)) return "react";
          if (/node_modules\/(react-hook-form|@hookform|zod)/.test(id)) return "forms";
          return undefined;
        },
      },
    },
  },
  test: { environment: "node", include: ["src/**/*.test.ts"] },
});
