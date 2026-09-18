#!/usr/bin/env bash

# Reproduce the ZenForge browser console from pinned DeepSeek Harness sources.
#
# The console ZenForge serves is a rebranded build of the upstream browser
# frontend: the shell (apps/web/dist) plus its client plugin bundles
# (packages/<group>/<pkg>/lib/client.js). This script is the whole recipe — it
# pins the upstream revision and package manager, refreshes a working tree,
# installs from the frozen lockfile, applies a reviewed brand patch with an
# assertion on every replacement, builds, stages the artifacts under webui/dsh/,
# and verifies the staged bytes. It never writes into docs/.
#
# The patch always starts from the pinned revision's pristine file contents: the
# touched files are restored with `git checkout --` before the patch runs, so a
# re-run is idempotent even after a previous run modified the tree.
set -euo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

# Pinned upstream identity. The revision is a full object name so the build is
# reproducible even if the branch moves; the version string is the repository
# package version at that revision (package.json "version").
UPSTREAM_URL=${DSH_CONSOLE_UPSTREAM:-https://github.com/deepseek-ai/deepseek-harness.git}
UPSTREAM_REV=ddefc45fbc7f8e46dd73185e68295696d1297887
UPSTREAM_REV_SHORT=ddefc45
UPSTREAM_VERSION=0.1.6-alpha.2
PNPM_VERSION=11.7.0

# Working tree and staging target. The tree defaults outside the repository so a
# build never mixes upstream sources into a ZenForge commit.
SRC=${DSH_CONSOLE_SRC:-/tmp/dsh-console-src}
STAGE="$REPO_ROOT/webui/dsh"

# Files the brand patch rewrites. They are restored to the pinned revision
# before the patch is applied, which is what makes the patch re-runnable.
PATCHED_FILES=(
  apps/web/public/manifest.webmanifest
  apps/web/public/favicon.svg
  apps/web/index.html
  packages/client/web/src/boot-page.ts
  packages/client/locale/src/locales/en.ts
  packages/client/locale/src/locales/zh.ts
  packages/client/ui-layout/src/client/AppFrame.tsx
  packages/client/ui-sidebar/src/client/SidebarRoot.tsx
  packages/client/ui-conversation/src/client/skeleton/EmptyHero.tsx
  packages/client/ui-plugin-manager/src/client/locales.ts
  packages/client/ui-settings-models/src/client/locales.ts
)

# Client plugin bundles left out of the staged console, as "<plugin>|<reason>".
# Each one is a heavy or experimental surface that the minimum chat roster does
# not need; the reasons are copied into webui/dsh/PROVENANCE.md so a reviewer
# can re-admit any of them by deleting its line.
DROP_PLUGINS=(
  'client/ui-sidebar-documentpreview|6.9 MB PDF engine plus the Office/document preview panel'
  'client/ui-sidebar-terminal|672 KB terminal emulator panel'
  'client/ui-trajectory|412 KB trajectory debug viewer'
  'client/ui-brand-official|upstream-official DeepSeek brand occupants; inert in the local profile'
  'extensions/cordis-client-runner|292 KB experimental extension runner'
  'experimental/inspector|128 KB experimental inspector'
  'experimental/client-ui-agent-team|172 KB experimental agent-team panel'
  'experimental/webworker-runtime|32 KB experimental worker runtime'
)

fail() {
  printf 'build-console: %s\n' "$*" >&2
  exit 1
}

say() {
  printf 'build-console: %s\n' "$*"
}

command -v git >/dev/null 2>&1 || fail "git is required"
command -v python3 >/dev/null 2>&1 || fail "python3 is required"
command -v corepack >/dev/null 2>&1 || fail "corepack is required (Node 22+ ships it)"

# --- working tree -----------------------------------------------------------
if [[ ! -d "$SRC/.git" ]]; then
  say "cloning $UPSTREAM_URL into $SRC"
  git clone --no-checkout "$UPSTREAM_URL" "$SRC" || fail "clone failed"
fi

head_rev=$(git -C "$SRC" rev-parse HEAD 2>/dev/null || true)
if [[ "$head_rev" != "$UPSTREAM_REV" ]]; then
  say "fetching $UPSTREAM_REV_SHORT"
  git -C "$SRC" fetch --depth=1 origin "$UPSTREAM_REV" || fail "fetch $UPSTREAM_REV failed"
  git -C "$SRC" checkout --force FETCH_HEAD || fail "checkout $UPSTREAM_REV failed"
fi
actual_rev=$(git -C "$SRC" rev-parse HEAD)
[[ "$actual_rev" == "$UPSTREAM_REV" ]] || fail "working tree at $actual_rev, want $UPSTREAM_REV"
say "working tree $SRC at $UPSTREAM_REV_SHORT"

# --- pnpm shim --------------------------------------------------------------
# The upstream build shells out to `pnpm` for its package scripts. corepack
# resolves pnpm@11.7.0 from the working tree's packageManager field, but only
# when invoked with that tree as the working directory, so the shim is a plain
# forwarder and every call below runs with cwd=$SRC.
SHIM_DIR=$(mktemp -d)
trap 'rm -rf "$SHIM_DIR"' EXIT
cat >"$SHIM_DIR/pnpm" <<'SHIM'
#!/bin/sh
exec corepack pnpm "$@"
SHIM
chmod +x "$SHIM_DIR/pnpm"
export PATH="$SHIM_DIR:$PATH"

resolved_pnpm=$(cd "$SRC" && pnpm --version)
[[ "$resolved_pnpm" == "$PNPM_VERSION" ]] || \
  fail "resolved pnpm $resolved_pnpm, want $PNPM_VERSION (check packageManager in $SRC)"
say "pnpm $resolved_pnpm"

# --- frozen install ---------------------------------------------------------
# The stamp binds the installed tree to the revision and lockfile digest, so a
# re-run with an unchanged tree skips a multi-minute install while a changed
# lockfile forces one.
LOCK_HASH=$( (cd "$SRC" && shasum -a 256 pnpm-lock.yaml) | awk '{print $1}')
INSTALL_STAMP="$SRC/.dsh-console-install-stamp"
WANT_STAMP="pnpm=$PNPM_VERSION rev=$UPSTREAM_REV lock=$LOCK_HASH"
if [[ ! -f "$INSTALL_STAMP" || "$(cat "$INSTALL_STAMP")" != "$WANT_STAMP" ]]; then
  say "installing with pnpm install --frozen-lockfile"
  (cd "$SRC" && pnpm install --frozen-lockfile) || fail "pnpm install --frozen-lockfile failed"
  printf '%s\n' "$WANT_STAMP" >"$INSTALL_STAMP"
else
  say "install stamp is current; skipping pnpm install"
fi

# --- brand patch ------------------------------------------------------------
say "restoring patched files to $UPSTREAM_REV_SHORT and applying the brand patch"
for relative in "${PATCHED_FILES[@]}"; do
  git -C "$SRC" checkout -- "$relative" || fail "git checkout -- $relative failed"
done

python3 - "$SRC" <<'PY'
"""Apply the ZenForge brand patch to a pristine upstream tree.

Every replacement asserts that the exact upstream text is present the expected
number of times before it writes, so a drifted source file fails the build
instead of silently staging a partially rebranded console.
"""

import pathlib
import sys

root = pathlib.Path(sys.argv[1])
patched: list[str] = []


def replace(rel: str, old: str, new: str, count: int = 1) -> None:
    path = root / rel
    assert path.is_file(), f"patch target missing: {rel}"
    text = path.read_text(encoding="utf-8")
    found = text.count(old)
    assert found == count, f"{rel}: expected {count} occurrence(s) of {old!r}, found {found}"
    path.write_text(text.replace(old, new), encoding="utf-8")
    if rel not in patched:
        patched.append(rel)
    print(f"[patch] {rel}: {old!r} -> {new!r}")


def splice(rel: str, start: str, end: str, new: str) -> None:
    path = root / rel
    text = path.read_text(encoding="utf-8")
    assert text.count(start) == 1, f"{rel}: start anchor {start!r} is not unique"
    begin = text.index(start)
    stop = text.index(end, begin)
    path.write_text(text[:begin] + new + text[stop:], encoding="utf-8")
    if rel not in patched:
        patched.append(rel)
    print(f"[patch] {rel}: replaced the block between {start!r} and {end!r}")


# The ZenForge mark: a bold Z on the 24-unit design grid, two bars and a
# diagonal, monochrome so `currentColor` follows the surrounding theme.
ZENFORGE_MARK_PATH = "M4 4h16v3.2L9.4 17H20v3H4v-3.2L14.6 7H4z"

ZENFORGE_FAVICON = """<svg xmlns="http://www.w3.org/2000/svg" width="50" height="50" viewBox="0 0 50 50" fill="none" role="img" aria-label="ZenForge">
\t<style>
\t\t@media (prefers-color-scheme: dark) {
\t\t\trect { fill: #f8fafc; }
\t\t\tpath { fill: #0f172a; }
\t\t}
\t</style>
\t<rect width="50" height="50" rx="11" fill="#0f172a"/>
\t<path d="M13 13h24v4.8L20.1 35H37v5H13v-4.8L26.9 18H13z" fill="#f8fafc"/>
</svg>
"""

# Manifest: the installable-app name and short name are the console's public
# identity, so both leave the upstream product name behind.
replace("apps/web/public/manifest.webmanifest", '"name": "DeepSeek Harness"', '"name": "ZenForge"')
replace("apps/web/public/manifest.webmanifest", '"short_name": "DSH"', '"short_name": "ZenForge"')

# Favicon: the upstream whale is replaced wholesale by the ZenForge mark. The
# assertion pins the upstream whale path so a changed upstream favicon fails
# loudly instead of being overwritten blind.
favicon_path = root / "apps/web/public/favicon.svg"
favicon_upstream = favicon_path.read_text(encoding="utf-8")
assert "M48.8354 10.0479" in favicon_upstream, \
    "apps/web/public/favicon.svg: upstream whale path anchor missing"
favicon_path.write_text(ZENFORGE_FAVICON, encoding="utf-8")
patched.append("apps/web/public/favicon.svg")
print("[patch] apps/web/public/favicon.svg: upstream whale -> ZenForge mark")

# The Vite title default. The build also injects DSH_CLIENT_TITLE, but the
# source default must not carry the upstream name either.
replace("apps/web/index.html", "<title>DSH Local Build</title>", "<title>ZenForge</title>")

# Boot page: the wordmark drawn before React arrives.
replace(
    "packages/client/web/src/boot-page.ts",
    "div(css.wordmark, 'HARNESS')",
    "div(css.wordmark, 'ZENFORGE')",
)

# Locale seats: the local-build brand string in both shipped locales.
replace(
    "packages/client/locale/src/locales/en.ts",
    "'brand.localBuild': 'DSH Local Build'",
    "'brand.localBuild': 'ZenForge Local Build'",
)
replace(
    "packages/client/locale/src/locales/zh.ts",
    "'brand.localBuild': 'DSH 本地构建'",
    "'brand.localBuild': 'ZenForge 本地构建'",
)

# Frame title: the pinned build title wins, and the fallback becomes ZenForge
# rather than the locale default. `t` existed only for that fallback, so its
# destructuring is dropped with it (noUnusedParameters would otherwise fail).
replace(
    "packages/client/ui-layout/src/client/AppFrame.tsx",
    "  renderSlot,\n  t,\n}: AppFrameProps) {",
    "  renderSlot,\n}: AppFrameProps) {",
)
replace(
    "packages/client/ui-layout/src/client/AppFrame.tsx",
    "const productTitle = process.env.DSH_CLIENT_TITLE ?? t('brand.localBuild')",
    "const productTitle = process.env.DSH_CLIENT_TITLE ?? 'ZenForge'",
)

# Sidebar: the slot fallback mark stops drawing the upstream whale, and the
# brand-name fallback follows the patched brand.localBuild locale string.
replace(
    "packages/client/ui-sidebar/src/client/SidebarRoot.tsx",
    "  FishLogo, IconNewChatOutline16, IconPanelLeftOutline16, isDarwinDesktop, Tooltip,",
    "  IconNewChatOutline16, IconPanelLeftOutline16, isDarwinDesktop, Tooltip,",
)
replace(
    "packages/client/ui-sidebar/src/client/SidebarRoot.tsx",
    "import css from './SidebarRoot.module.css'\n",
    "import css from './SidebarRoot.module.css'\n\n"
    "/** ZenForge brand mark on a 24-unit grid (replaces the upstream whale). */\n"
    "function ZenForgeMark({ size = 24 }: { size?: number }) {\n"
    "  return (\n"
    '    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" aria-hidden="true">\n'
    f'      <path d="{ZENFORGE_MARK_PATH}" fill="currentColor" />\n'
    "    </svg>\n"
    "  )\n"
    "}\n",
)
replace(
    "packages/client/ui-sidebar/src/client/SidebarRoot.tsx",
    "<FishLogo size={24} />",
    "<ZenForgeMark size={24} />",
    count=2,
)

# Conversation hero: the animated whale becomes the static ZenForge mark. The
# whale's regional-deformation morph targets and the fish imports are removed
# with it, so no whale geometry survives in this module.
splice(
    "packages/client/ui-conversation/src/client/skeleton/EmptyHero.tsx",
    "/* Hover swim morph targets:",
    "function HeroFish(",
    "/* ZenForge brand mark on the 24-unit design grid, drawn monochrome so\n"
    "   `currentColor` follows the theme. */\n"
    'const ZENFORGE_MARK_VIEWBOX = { width: 24, height: 24 }\n'
    f"const ZENFORGE_MARK_PATH = '{ZENFORGE_MARK_PATH}'\n\n"
    "/**\n"
    " * The hero brand mark (34px square), static: the rebranded replacement for\n"
    " * the upstream whale. Decorative — hidden from the accessibility tree.\n"
    " * @param props.hovering - slot compatibility; the static mark does not animate.\n"
    " * @returns the brand svg element.\n"
    " */\n",
)
splice(
    "packages/client/ui-conversation/src/client/skeleton/EmptyHero.tsx",
    "function HeroFish({ hovering }: { hovering: boolean }) {",
    "/**\n * Render the hero chrome",
    "function HeroFish({ hovering }: { hovering: boolean }) {\n"
    "  return (\n"
    "    <svg\n"
    "      className={css.fish}\n"
    "      data-hovering={hovering ? 'true' : 'false'}\n"
    "      width={34}\n"
    "      height={34}\n"
    "      viewBox={`0 0 ${ZENFORGE_MARK_VIEWBOX.width} ${ZENFORGE_MARK_VIEWBOX.height}`}\n"
    "      fill=\"none\"\n"
    "      aria-hidden=\"true\"\n"
    "    >\n"
    "      <path d={ZENFORGE_MARK_PATH} fill=\"currentColor\" />\n"
    "    </svg>\n"
    "  )\n"
    "}\n\n",
)
replace(
    "packages/client/ui-conversation/src/client/skeleton/EmptyHero.tsx",
    "  FISH_LOGO_PATH, FISH_LOGO_VIEWBOX, IconChevronDownOutline14, IconFolderClose16, IconFolderOpen16,",
    "  IconChevronDownOutline14, IconFolderClose16, IconFolderOpen16,",
)

# Retained plugin bundles whose copy names the upstream product. These are not
# dropped, so their user-facing strings are rebranded too.
replace(
    "packages/client/ui-plugin-manager/src/client/locales.ts",
    "来源不明的插件可能损坏 DeepSeek Harness，或读取和泄露你的数据。",
    "来源不明的插件可能损坏 ZenForge，或读取和泄露你的数据。",
)
replace(
    "packages/client/ui-plugin-manager/src/client/locales.ts",
    "they run with your permissions and can damage DeepSeek Harness or leak your data.",
    "they run with your permissions and can damage ZenForge or leak your data.",
)
replace(
    "packages/client/ui-settings-models/src/client/locales.ts",
    "DeepSeek Harness 0.1 remains in testing for Harness developers.",
    "ZenForge 0.1 remains in testing for ZenForge developers.",
)
replace(
    "packages/client/ui-settings-models/src/client/locales.ts",
    "DeepSeek Harness's core plugins and foundational APIs will continue to evolve rapidly over the coming months.",
    "ZenForge's core plugins and foundational APIs will continue to evolve rapidly over the coming months.",
)
replace(
    "packages/client/ui-settings-models/src/client/locales.ts",
    "We welcome Harness developers everywhere to join the DSH plugin ecosystem.",
    "We welcome ZenForge developers everywhere to join the plugin ecosystem.",
)
replace(
    "packages/client/ui-settings-models/src/client/locales.ts",
    "DeepSeek Harness 目前的 0.1 版本仍处在面向 Harness 开发者进行测试的阶段",
    "ZenForge 目前的 0.1 版本仍处在面向 ZenForge 开发者进行测试的阶段",
)
replace(
    "packages/client/ui-settings-models/src/client/locales.ts",
    "预计 DeepSeek Harness 的核心插件以及基础 API 都会在接下来的一段时间内快速迭代、持续演化。",
    "预计 ZenForge 的核心插件以及基础 API 都会在接下来的一段时间内快速迭代、持续演化。",
)
replace(
    "packages/client/ui-settings-models/src/client/locales.ts",
    "欢迎全球 Harness 开发者加入 DSH 插件生态。",
    "欢迎全球 ZenForge 开发者加入插件生态。",
)

print(f"[patch] applied to {len(patched)} file(s)")
PY

# --- build ------------------------------------------------------------------
say "building with DSH_CLIENT_TITLE=ZenForge DSH_CLIENT_BUILD_PROFILE=local"
(cd "$SRC" && DSH_CLIENT_TITLE=ZenForge DSH_CLIENT_BUILD_PROFILE=local pnpm run build) || \
  fail "upstream build failed"

DIST="$SRC/apps/web/dist"
[[ -f "$DIST/index.html" ]] || fail "build produced no $DIST/index.html"

# --- stage ------------------------------------------------------------------
say "staging the shell into $STAGE (excluding *.map and dist/preview/)"
rm -rf "$STAGE/assets" "$STAGE/plugins"
rm -f "$STAGE/index.html" "$STAGE/manifest.webmanifest" "$STAGE/favicon.svg" "$STAGE/PROVENANCE.md"
mkdir -p "$STAGE"

cp "$DIST/index.html" "$STAGE/index.html"
cp "$DIST/manifest.webmanifest" "$STAGE/manifest.webmanifest"
cp "$DIST/favicon.svg" "$STAGE/favicon.svg"
# Source maps and the worker preview page are debug/secondary surfaces and are
# deliberately not staged.
(cd "$DIST" && tar cf - --exclude '*.map' assets) | (cd "$STAGE" && tar xf -)

plugin_count=0
for bundle in $(find "$SRC/packages" -mindepth 3 -maxdepth 4 -type f -path '*/lib/client*.js'); do
  relative=${bundle#"$SRC/packages/"}
  plugin=${relative%%/lib/*}
  base=${relative##*/}
  dropped=false
  for drop in "${DROP_PLUGINS[@]}"; do
    if [[ "$plugin" == "${drop%%|*}" ]]; then dropped=true; break; fi
  done
  if [[ "$dropped" == true ]]; then continue; fi
  destination="$STAGE/plugins/$plugin/$base"
  mkdir -p "$(dirname "$destination")"
  cp "$bundle" "$destination"
  plugin_count=$((plugin_count + 1))
done
[[ "$plugin_count" -gt 0 ]] || fail "no client plugin bundles were staged"
say "staged $plugin_count client plugin bundle(s)"

# --- provenance -------------------------------------------------------------
artifact_files=$(find "$STAGE" -type f ! -name 'PROVENANCE.md' | wc -l | tr -d ' ')
artifact_size=$(du -sh "$STAGE" | awk '{print $1}')
dropped_list=''
for drop in "${DROP_PLUGINS[@]}"; do
  dropped_list+="- \`packages/$drop\`"$'\n'
done

cat >"$STAGE/PROVENANCE.md" <<EOF
# ZenForge console provenance

This directory is the staged output of \`scripts/build-console.sh\`. It is a
rebranded build of the DeepSeek Harness browser console; the script is the
recipe and this file records what it produced.

- **Upstream repository**: https://github.com/deepseek-ai/deepseek-harness
- **Upstream revision**: \`$UPSTREAM_REV\` (short \`$UPSTREAM_REV_SHORT\`)
- **Upstream version**: \`$UPSTREAM_VERSION\`
- **License**: MIT — Copyright (c) 2026 DeepSeek. The upstream \`LICENSE\` and
  the repository's \`THIRD_PARTY_NOTICES.md\` carry the attribution.
- **Build command**: \`DSH_CLIENT_TITLE=ZenForge DSH_CLIENT_BUILD_PROFILE=local pnpm run build\`
  run from the pinned working tree with pnpm \`$PNPM_VERSION\`.

## Patched files

Every replacement is asserted by \`scripts/build-console.sh\` before it is
written; the patch starts from the pinned revision's pristine files.

- \`apps/web/public/manifest.webmanifest\` — \`name\` "DeepSeek Harness" → "ZenForge"; \`short_name\` "DSH" → "ZenForge"
- \`apps/web/public/favicon.svg\` — upstream whale replaced by a ZenForge Z mark
- \`apps/web/index.html\` — \`<title>DSH Local Build</title>\` → \`<title>ZenForge</title>\`
- \`packages/client/web/src/boot-page.ts\` — boot wordmark \`HARNESS\` → \`ZENFORGE\`
- \`packages/client/locale/src/locales/en.ts\` — \`brand.localBuild\` "DSH Local Build" → "ZenForge Local Build"
- \`packages/client/locale/src/locales/zh.ts\` — \`brand.localBuild\` "DSH 本地构建" → "ZenForge 本地构建"
- \`packages/client/ui-layout/src/client/AppFrame.tsx\` — \`productTitle\` fallback → "ZenForge"
- \`packages/client/ui-sidebar/src/client/SidebarRoot.tsx\` — whale fallback mark → ZenForge mark
- \`packages/client/ui-conversation/src/client/skeleton/EmptyHero.tsx\` — whale hero → static ZenForge mark
- \`packages/client/ui-plugin-manager/src/client/locales.ts\` — "DeepSeek Harness" copy → "ZenForge"
- \`packages/client/ui-settings-models/src/client/locales.ts\` — "DeepSeek Harness"/"Harness developers"/"DSH plugin ecosystem" copy → ZenForge wording

## Dropped files

- \`apps/web/dist/**/*.map\` — JavaScript source maps (debug-only, ~13 MB).
- \`apps/web/dist/preview/\` and \`apps/web/dist/preview.html\` — the worker preview bundle (secondary surface).
$dropped_list
## Deliberate upstream references

- \`@deepseek-ai/...\` module specifiers in the shell and plugin bundles are the
  loader's external module ids; they are internal identifiers, not product
  branding, and renaming them would break bundle resolution.
- The upstream whale SVG path data remains in the unmodified shared primitives
  bundle; only the rendered marks that the console displays were rebranded.
- This file names the upstream project and its license, as attribution requires.

## Artifact inventory

- Files staged (excluding this file): $artifact_files
- Client plugin bundles staged: $plugin_count
- Total directory size: $artifact_size
EOF

# --- verification -----------------------------------------------------------
verify_failures=0

# 1. The ZenForge identity is present where users see it.
grep -q '<title>ZenForge</title>' "$STAGE/index.html" || \
  { printf 'verify: shell title is not ZenForge\n' >&2; verify_failures=$((verify_failures + 1)); }
grep -q '"name": "ZenForge"' "$STAGE/manifest.webmanifest" || \
  { printf 'verify: manifest name is not ZenForge\n' >&2; verify_failures=$((verify_failures + 1)); }
grep -q 'ZenForge' "$STAGE/favicon.svg" || \
  { printf 'verify: favicon does not carry the ZenForge mark\n' >&2; verify_failures=$((verify_failures + 1)); }

# 2. Upstream product names must not survive in any served asset. PROVENANCE.md
#    is excluded because attribution requires naming the upstream project.
if grep -rIl -e 'DeepSeek Harness' -e 'DSH Local Build' -e 'HARNESS' \
  "$STAGE/index.html" "$STAGE/manifest.webmanifest" "$STAGE/favicon.svg" \
  "$STAGE/assets" "$STAGE/plugins"; then
  printf 'verify: an upstream product name survived in the staged console\n' >&2
  verify_failures=$((verify_failures + 1))
fi

# 3. No external URL may be referenced by the shell or manifest.
if grep -nE 'https?://|src="//|href="//' "$STAGE/index.html" "$STAGE/manifest.webmanifest"; then
  printf 'verify: the staged shell references an external URL\n' >&2
  verify_failures=$((verify_failures + 1))
fi

# 4. Every local reference in the shell must resolve to a staged file.
while IFS= read -r reference; do
  case "$reference" in
    ./*) target="$STAGE/${reference#./}" ;;
    /*)  target="$STAGE/${reference#/}" ;;
    *)   printf 'verify: shell reference %s is not local\n' "$reference" >&2
         verify_failures=$((verify_failures + 1)); continue ;;
  esac
  [[ -f "$target" ]] || {
    printf 'verify: shell reference %s has no staged file\n' "$reference" >&2
    verify_failures=$((verify_failures + 1))
  }
done < <(grep -oE '(src|href)="[^"]+"' "$STAGE/index.html" | sed -E 's/^(src|href)="//; s/"$//')

[[ "$verify_failures" -eq 0 ]] || fail "$verify_failures verification check(s) failed"

say "verified: $artifact_files artifact(s), $plugin_count plugin bundle(s), $artifact_size, no upstream product name staged"
say "done"