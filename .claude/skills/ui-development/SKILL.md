---
name: ui-development
description: How CypherUI (web/) is built — shadcn/Base UI components, Tailwind tokens, theming, the generated API client, and accessibility. Use when writing or changing any React/Next.js code in web/.
---

# UI Development

## Stack facts (verify before assuming)

- **Next.js 16, App Router, Turbopack, `output: "standalone"`.** This version has breaking changes vs. older Next — `web/AGENTS.md` says read `node_modules/next/dist/docs/` before using an unfamiliar API. Don't assume Pages Router or older config shapes.
- **This shadcn scaffold is built on Base UI (`@base-ui/react`), NOT Radix.** Consequences that will bite you:
  - Triggers use a `render` prop, not `asChild`: `<DialogTrigger render={<Button />}>Label</DialogTrigger>` (children pass through). `asChild` is a type error.
  - `Select.onValueChange` yields `string | null` — coerce (`v ?? ""`) when writing into string state.
  - Check the component file in `components/ui/` for its real prop types before guessing; they mirror Base UI, not Radix.
- Tailwind v4, `next-themes` for light/dark/system, Lucide for icons, TanStack Query for server state.

## Non-negotiable conventions

- **Never hardcode colors.** Use theme tokens (`bg-primary`, `text-muted-foreground`, `border-destructive`, …) so white-labeling stays a token swap, not a code change. No hex/`rgb()` in components.
- **No raw `fetch` in components.** All API access goes through `web/lib/api.ts`, whose response/request types are **generated from the OpenAPI spec** (`npm run gen:api`). When the backend API changes: `make openapi` (repo root) → `npm run gen:api` (web) → use the regenerated `components["schemas"][...]` types. Never hand-type an endpoint shape.
- **No CORS.** The UI reaches the API via same-origin `/api/*`, proxied to CypherCore by the `rewrites` in `next.config.ts` (`CYPHER_CORE_API_URL`). Don't add cross-origin fetches.
- Data fetching uses `useQuery`/`useMutation` with stable `queryKey`s; invalidate related keys on mutation success. Auth is handled in `lib/api.ts` (in-memory access token + single-use refresh rotation + one-shot 401 retry) — components never touch tokens.

## Structure

- Route groups: unauthenticated pages at top level (`/login`), the authed admin app under `app/(shell)/` whose `layout.tsx` guards the session client-side and renders the sidebar. Add new admin pages under `(shell)/`.
- Providers (theme + QueryClient) live in `app/providers.tsx`, mounted once in the root layout with `suppressHydrationWarning` on `<html>` for theme.

## Accessibility bar (WCAG 2.1 AA)

Every interactive control needs an accessible name (`aria-label` on icon-only buttons), visible focus states (Base UI + the button variants provide these — don't strip them), keyboard operability, and contrast-safe token pairs in both themes. Forms use `<Label htmlFor>` tied to inputs.

## i18n (as it lands)

User-facing strings will move to `next-intl` — do not scatter hardcoded UI strings; keep them extractable. (Scaffolding is Phase 2+; until wired, still avoid embedding copy in logic.)
