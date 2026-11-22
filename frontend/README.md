# BTN Operations — Web app

React 19 + TypeScript + Vite + Tailwind CSS 4, React Router 7, TanStack Query. Talks to the Go API in
`../backend` through `/api` (Vite proxies it to `localhost:4000` in development).

```bash
pnpm install
pnpm dev          # http://localhost:5173
pnpm typecheck    # tsc
pnpm test         # vitest (includes the en/am/rw translation completeness checks)
pnpm build        # production build in dist/
```

- Modules live in `src/modules/<module>/` (lazy-loaded routes in `routes.tsx`); the sidebar is `src/app/nav.ts`.
- All text goes through `useT()` with keys in `src/i18n/ns/*` — every key must exist in English, Amharic and Kinyarwanda.
- Shared UI is in `src/components/ui`; branding (colours, font, size, logos) is applied at runtime from `/api/public/branding`.

See `../docs/ARCHITECTURE.md` for the full conventions.
