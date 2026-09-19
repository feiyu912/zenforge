# zenforge console provenance

This directory is the staged output of `scripts/build-console.sh`. It is a
rebranded build of the DeepSeek Harness browser console; the script is the
recipe and this file records what it produced.

- **Upstream repository**: https://github.com/deepseek-ai/deepseek-harness
- **Upstream revision**: `ddefc45fbc7f8e46dd73185e68295696d1297887` (short `ddefc45`)
- **Upstream version**: `0.1.6-alpha.2`
- **License**: MIT — Copyright (c) 2026 DeepSeek. The upstream `LICENSE` and
  the repository's `THIRD_PARTY_NOTICES.md` carry the attribution.
- **Build command**: `DSH_CLIENT_TITLE=zenforge DSH_CLIENT_BUILD_PROFILE=local pnpm run build`
  run from the pinned working tree with pnpm `11.7.0`.

## Patched files

Every replacement is asserted by `scripts/build-console.sh` before it is
written; the patch starts from the pinned revision's pristine files.

- `apps/web/public/manifest.webmanifest` — `name` "DeepSeek Harness" → "zenforge"; `short_name` "DSH" → "zenforge"
- `apps/web/public/favicon.svg` — upstream whale replaced by a zenforge Z mark
- `apps/web/index.html` — `<title>DSH Local Build</title>` → `<title>zenforge</title>`
- `packages/client/web/src/boot-page.ts` — boot wordmark `HARNESS` → `ZENFORGE`
- `packages/client/locale/src/locales/en.ts` — `brand.localBuild` "DSH Local Build" → "zenforge Local Build"
- `packages/client/locale/src/locales/zh.ts` — `brand.localBuild` "DSH 本地构建" → "zenforge 本地构建"
- `packages/client/ui-layout/src/client/AppFrame.tsx` — `productTitle` fallback → "zenforge"
- `packages/client/ui-sidebar/src/client/SidebarRoot.tsx` — whale fallback mark → zenforge mark
- `packages/client/ui-conversation/src/client/skeleton/EmptyHero.tsx` — whale hero → static zenforge mark
- `packages/client/ui-plugin-manager/src/client/locales.ts` — "DeepSeek Harness" copy → "zenforge"
- `packages/client/ui-settings-models/src/client/locales.ts` — "DeepSeek Harness"/"Harness developers"/"DSH plugin ecosystem" copy → zenforge wording

## Dropped files

- `apps/web/dist/**/*.map` — JavaScript source maps (debug-only, ~13 MB).
- `apps/web/dist/preview/` and `apps/web/dist/preview.html` — the worker preview bundle (secondary surface).
- `packages/client/ui-sidebar-documentpreview`
- `packages/client/ui-sidebar-terminal`
- `packages/client/ui-trajectory`
- `packages/client/ui-brand-official`
- `packages/extensions/cordis-client-runner`
- `packages/experimental/inspector`
- `packages/experimental/client-ui-agent-team`
- `packages/experimental/webworker-runtime`

## Deliberate upstream references

- `@deepseek-ai/...` module specifiers in the shell and plugin bundles are the
  loader's external module ids; they are internal identifiers, not product
  branding, and renaming them would break bundle resolution.
- The upstream whale SVG path data remains in the unmodified shared primitives
  bundle; only the rendered marks that the console displays were rebranded.
- This file names the upstream project and its license, as attribution requires.

## Artifact inventory

- Files staged (excluding this file): 144
- Client plugin bundles staged: 55
- Total directory size: 8.8M
