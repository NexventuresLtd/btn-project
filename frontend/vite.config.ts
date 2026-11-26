import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { VitePWA } from "vite-plugin-pwa";
import path from "node:path";

export default defineConfig({
  plugins: [
    react(),
    tailwindcss(),
    // Installable PWA + offline app shell (F4, SRS OFF-01/OFF-21). Service worker source: src-sw/sw.ts.
    VitePWA({
      strategies: "injectManifest",
      srcDir: "src-sw",
      filename: "sw.ts",
      registerType: "prompt",
      injectRegister: false,
      manifestFilename: "manifest.webmanifest",
      includeAssets: ["brand/*.png", "pwa/apple-touch-icon.png"],
      manifest: {
        id: "/",
        name: "BTN Operations",
        short_name: "BTN Ops",
        description: "BTN Manufacturing operations: production, stock, sales, workforce and finance — works offline.",
        lang: "en",
        start_url: "/",
        scope: "/",
        display: "standalone",
        orientation: "any",
        background_color: "#0C234B",
        theme_color: "#0C234B",
        categories: ["business", "productivity"],
        icons: [
          { src: "/pwa/icon-192.png", sizes: "192x192", type: "image/png", purpose: "any" },
          { src: "/pwa/icon-512.png", sizes: "512x512", type: "image/png", purpose: "any" },
          { src: "/pwa/maskable-192.png", sizes: "192x192", type: "image/png", purpose: "maskable" },
          { src: "/pwa/maskable-512.png", sizes: "512x512", type: "image/png", purpose: "maskable" },
        ],
      },
      injectManifest: {
        globPatterns: ["**/*.{js,css,html,svg,png,jpg,ico,woff,woff2,webmanifest}"],
        globIgnores: ["**/*.map"],
        maximumFileSizeToCacheInBytes: 6 * 1024 * 1024,
      },
      devOptions: { enabled: false },
    }),
  ],
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
          if (/node_modules\/(dexie|workbox-window)/.test(id)) return "offline";
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
