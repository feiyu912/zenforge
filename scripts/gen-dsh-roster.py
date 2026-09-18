#!/usr/bin/env python3
"""Regenerate internal/dshmount/roster.json from the pinned upstream tree.

The ZenForge console is staged by scripts/build-console.sh as
``webui/dsh/plugins/<dir>/client.js``. Those bytes carry no graph metadata: a
client bundle is a classic script whose only payload is
``window.__ModuleLoader__.load({id, factory})``. Everything the boot graph needs
per entry is recovered from two places:

* the package id, the ``inject`` package edges, and the ``immediately`` prefetch
  mark come from the owning package's ``package.json`` under ``dsh.client``;
* the ``external`` module requests are scanned from the shipped bundle itself,
  because the factory resolves its dependencies by name at activation time
  (``require("@deepseek-ai/dsh-client-ui-primitives")``) and a bundle that is
  served but whose module requests are not on the graph cannot materialize.

Upstream composes the graph in packages/client/modules/src/index.ts:

* ``resolveMeta`` (packages/client/modules/src/index.ts:781-816) reads
  ``dsh.client`` through ``parseDshClient`` (client/manifest.ts:158-177) and
  keeps only ``platform === 'web'`` declarations;
* ``graphRow`` (index.ts:438-448) copies ``inject``/``immediately``/``external``
  onto the wire row verbatim;
* ``orderByModuleGraph`` (index.ts:460-492) orders rows by the ``external``
  edges, aliasing ``<pkg>/client`` onto the bare package row and ignoring a
  specifier no row provides (the shell seeds);
* ``compose`` (index.ts:718-767) pins ``@deepseek-ai/dsh-client-modules`` as the
  bootstrap batch and partitions the rest into application batches.

The shell seeds the platform singletons (react, the client store, the slots and
primitives packages, cordis) through its vendor bundle; a bundle requiring one
of those needs no graph row. Which specifiers those are is not hard-coded here:
every scanned specifier that is not a graph row is looked up in the staged
assets, and the generator fails if it cannot be found, so a rebuilt shell that
drops a seed is a loud error rather than an unaccounted dependency.

This script reads manifests and staged bundles only; it never runs the Node
build.

Usage:
    python3 scripts/gen-dsh-roster.py [--src /tmp/dsh-src] [--stage webui/dsh]
                                      [--out internal/dshmount/roster.json]
"""

from __future__ import annotations

import argparse
import json
import pathlib
import re
import subprocess
import sys

UPSTREAM_REV = "ddefc45fbc7f8e46dd73185e68295696d1297887"
UPSTREAM_REV_SHORT = "ddefc45"
UPSTREAM_VERSION = "0.1.6-alpha.2"
UPSTREAM_REPOSITORY = "https://github.com/deepseek-ai/deepseek-harness"

# Staged bundles deliberately withheld from the advertised graph, as
# "<dir>|<reason>". A client entry whose activation cannot succeed is fatal:
# boot-client's assertEntriesActive audit
# (packages/client/web/src/boot-client.ts:63-83) reports every non-active entry
# and rejects the whole console, which is the console's boot-failure page. DSH's
# designed degradation for an unavailable panel is to leave the plugin OUT of the
# graph — the surface that would have registered falls back to its empty state.
#
# Two exclusions are recorded here, each with the evidence for it:
#
# * extensions/ui-cordis exports a Cordis inject of dynamicCordisRunner and
#   remote.dynamicCordisRunner; their only provider is
#   @deepseek-ai/dsh-cordis-client-runner
#   (packages/extensions/cordis-client-runner/src/client/index.ts:291
#    ctx.provide('dynamicCordisRunner', face); the remote namespace is
#    registered in its client test, tests/plugin.client.spec.ts:172), and
#   scripts/build-console.sh DROP_PLUGINS does not stage that package.
#
# * client/ui-directory-picker-browse and client/ui-directory-picker-native are
#   the two browser directory-picker panels. A running host (serve end to end)
#   reported exactly one entry failing activation,
#   @deepseek-ai/dsh-client-ui-directory-picker-browse, while every other entry
#   activated; its requires are only shell seeds, so the failure is an
#   activation/order dependency on the workspace picker slot rather than a
#   missing file. A picker panel is inert in a browser without a native
#   directory dialog, and removing the plugin is the protocol's designed
#   degradation, so both members of the family are withheld.
#
# The generator asserts each reason against the staged bytes below, so this
# table cannot silently outlive a rebuilt artifact.
BLOCKED = {
    "extensions/ui-cordis": (
        "its exported Cordis inject requires dynamicCordisRunner and "
        "remote.dynamicCordisRunner, whose only provider is "
        "@deepseek-ai/dsh-cordis-client-runner "
        "(packages/extensions/cordis-client-runner/src/client/index.ts:291), a package "
        "scripts/build-console.sh does not stage; advertising this entry would leave it "
        "pending and make boot-client's activation audit reject the whole console"
    ),
    "client/ui-directory-picker-browse": (
        "a running host reported this entry (and only this entry) failing activation; its "
        "requires are shell seeds, so the failure is an activation/order dependency on the "
        "workspace directory-flow slot rather than a missing module. A browser picker panel "
        "is inert here, and leaving a plugin out of the graph is DSH's designed degradation "
        "for an unavailable surface"
    ),
    "client/ui-directory-picker-native": (
        "the native directory-picker sibling of ui-directory-picker-browse: it registers the "
        "same workspace directory-flow slot for a native dialog no browser host here can "
        "provide, so it is withheld with it rather than left to fail activation"
    ),
}
# Evidence each excluded bundle must still show in its staged bytes.
BLOCKED_REQUIRED_SERVICE = {
    "extensions/ui-cordis": "dynamicCordisRunner",
}

# Real `require("...")` calls, as they appear in the generated factory body.
REQUIRE = re.compile(r"require\(\s*[\"']([^\"']+)[\"']\s*\)")
# The exported Cordis inject is emitted as either `exports.inject = [...]` or
# `exports.inject = <name>` naming a `const <name> = [...]` declared above it.
EXPORTED_INJECT = re.compile(r"exports\.inject\s*=\s*([A-Za-z_$][\w$]*|\[)")
INLINE_INJECT = re.compile(r"exports\.inject\s*=\s*\[([^\]]*)\]")
DECLARED_INJECT = re.compile(r"(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=\s*\[([^\]]*)\]")
STRING_LITERAL = re.compile(r"'([^']*)'|\"([^\"]*)\"")


def git_revision(src: pathlib.Path) -> str:
    try:
        return subprocess.run(
            ["git", "-C", str(src), "rev-parse", "HEAD"],
            check=True, capture_output=True, text=True,
        ).stdout.strip()
    except (OSError, subprocess.CalledProcessError) as error:
        raise SystemExit(f"gen-dsh-roster: cannot read the revision of {src}: {error}")


def code_spans(source: str) -> list[tuple[int, int]]:
    """Split a JS source into the spans that are executable code.

    A plain regex over the file also matches `require('x')` inside JSDoc
    comments (the inlined picomatch documentation does exactly this) and inside
    the template literal that renders a module-system error message. Only a
    match whose start lies in a code span is a real module request.
    """
    spans: list[tuple[int, int]] = []
    length = len(source)
    index = 0
    start = 0
    while index < length:
        char = source[index]
        if char == "/" and index + 1 < length and source[index + 1] == "/":
            spans.append((start, index))
            newline = source.find("\n", index)
            index = length if newline < 0 else newline + 1
            start = index
        elif char == "/" and index + 1 < length and source[index + 1] == "*":
            spans.append((start, index))
            close = source.find("*/", index + 2)
            index = length if close < 0 else close + 2
            start = index
        elif char in "\"'`":
            spans.append((start, index))
            quote = char
            index += 1
            while index < length:
                if source[index] == "\\":
                    index += 2
                    continue
                if source[index] == quote:
                    index += 1
                    break
                index += 1
            start = index
        else:
            index += 1
    spans.append((start, length))
    return spans


def required_specifiers(bundle: pathlib.Path) -> list[str]:
    source = bundle.read_text(encoding="utf-8", errors="replace")
    spans = code_spans(source)
    found: set[str] = set()
    for match in REQUIRE.finditer(source):
        specifier = match.group(1)
        if any(begin <= match.start() < end for begin, end in spans):
            found.add(specifier)
    return sorted(found)


def package_specifier(specifier: str) -> str:
    """Normalize `<pkg>/client` onto the bare package row it aliases."""
    return specifier[: -len("/client")] if specifier.endswith("/client") else specifier


def upstream_manifests(src: pathlib.Path) -> dict[str, tuple[str, dict]]:
    """Map packages/<dir> to (name, dsh.client) for every web client package."""
    packages = src / "packages"
    if not packages.is_dir():
        raise SystemExit(f"gen-dsh-roster: {packages} is not a directory; pass --src")
    found: dict[str, tuple[str, dict]] = {}
    for manifest in sorted(packages.rglob("package.json")):
        if "node_modules" in manifest.parts:
            continue
        try:
            parsed = json.loads(manifest.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as error:
            raise SystemExit(f"gen-dsh-roster: {manifest}: {error}")
        client = (parsed.get("dsh") or {}).get("client")
        if not isinstance(client, dict) or client.get("platform") != "web":
            continue
        relative = manifest.parent.relative_to(packages).as_posix()
        found[relative] = (parsed.get("name"), client)
    return found


def staged_bundles(stage: pathlib.Path) -> dict[str, pathlib.Path]:
    """Map plugins/<dir> to the staged client.js for every staged bundle."""
    plugins = stage / "plugins"
    if not plugins.is_dir():
        raise SystemExit(f"gen-dsh-roster: {plugins} is not a directory; pass --stage")
    found: dict[str, pathlib.Path] = {}
    for bundle in sorted(plugins.rglob("client.js")):
        relative = bundle.parent.relative_to(plugins).as_posix()
        found[relative] = bundle
    return found


def staged_assets(stage: pathlib.Path) -> list[pathlib.Path]:
    assets = stage / "assets"
    if not assets.is_dir():
        raise SystemExit(f"gen-dsh-roster: {assets} is not a directory; pass --stage")
    return sorted(assets.rglob("*.js"))


def shell_provider(specifier: str, assets: list[pathlib.Path], stage: pathlib.Path) -> str | None:
    """Return the staged asset that seeds specifier, or None when none does.

    The shell's vendor bundle holds the platform seed table, so a specifier the
    graph does not provide must appear in one of the staged scripts. The check
    is a substring lookup: a rewritten shell that drops a seed then fails
    generation instead of serving a bundle that cannot materialize.
    """
    for asset in assets:
        if specifier in asset.read_text(encoding="utf-8", errors="replace"):
            return asset.relative_to(stage).as_posix()
    return None


def _services(bracketed: str) -> list[str]:
    return [first or second for first, second in STRING_LITERAL.findall(bracketed)]


def exported_cordis_inject(bundle: pathlib.Path) -> list[str]:
    text = bundle.read_text(encoding="utf-8", errors="replace")
    inline = INLINE_INJECT.findall(text)
    if inline:
        return _services(inline[-1])
    names = EXPORTED_INJECT.findall(text)
    if not names:
        return []
    declared = {name: body for name, body in DECLARED_INJECT.findall(text)}
    return _services(declared.get(names[-1], ""))


def build_roster(src: pathlib.Path, stage: pathlib.Path) -> dict:
    revision = git_revision(src)
    if revision != UPSTREAM_REV:
        raise SystemExit(
            f"gen-dsh-roster: {src} is at {revision}, want {UPSTREAM_REV}; "
            "the staged console was built from that revision and the roster must match it"
        )

    manifests = upstream_manifests(src)
    bundles = staged_bundles(stage)
    assets = staged_assets(stage)

    missing = sorted(dir for dir in bundles if dir not in manifests)
    if missing:
        raise SystemExit(
            "gen-dsh-roster: staged bundle(s) have no upstream dsh.client manifest: "
            + ", ".join(missing)
        )

    # Every advertised row id, used to tell a module request that needs an
    # ordering edge from one the shell already seeds.
    row_ids = {manifests[dir][0] for dir in bundles}

    shell_provided: dict[str, str] = {}
    entries = []
    for relative, bundle in bundles.items():
        package, client = manifests[relative]
        if not package:
            raise SystemExit(f"gen-dsh-roster: {relative} has no package name")

        external: list[str] = []
        for specifier in required_specifiers(bundle):
            if specifier.startswith("."):
                # Package-local chunk request; no chunk files are staged, so it
                # is not a module-table request.
                continue
            bare = package_specifier(specifier)
            if bare == package:
                raise SystemExit(
                    f"gen-dsh-roster: {relative} requires its own package {specifier!r}; "
                    "a row must not declare its own package (orderByModuleGraph rejects it)"
                )
            # Every real module request is recorded, exactly as the factory
            # makes it. A specifier that names another row is an ordering edge
            # (BuildGraph orders by external); one the shell seeds is ignored by
            # orderByModuleGraph and skipped by arriveGraphRow, but recording it
            # keeps the graph an accounting of what the bundles actually ask for.
            external.append(specifier)
            if bare in row_ids:
                continue
            provider = shell_provider(specifier, assets, stage)
            if provider is None:
                raise SystemExit(
                    f"gen-dsh-roster: {relative} requires {specifier!r}, which is neither a "
                    "roster package nor present in the staged shell assets; stage the provider "
                    "or record a documented exclusion"
                )
            shell_provided[specifier] = provider

        row: dict[str, object] = {"dir": relative, "id": package}
        inject = client.get("inject")
        if inject:
            row["inject"] = list(inject)
        if external:
            row["external"] = external
        if client.get("immediately") is True:
            row["immediately"] = True
        entries.append(row)

    blocked = []
    for relative, reason in sorted(BLOCKED.items()):
        if relative not in bundles:
            raise SystemExit(
                f"gen-dsh-roster: blocked entry {relative} is not staged; remove it from BLOCKED"
            )
        required = BLOCKED_REQUIRED_SERVICE.get(relative)
        if required is not None:
            services = exported_cordis_inject(bundles[relative])
            if required not in services:
                raise SystemExit(
                    f"gen-dsh-roster: blocked entry {relative} no longer exports {required!r} "
                    f"(inject={services}); revisit the exclusion"
                )
        blocked.append({"dir": relative, "id": manifests[relative][0], "reason": reason})

    omitted = [
        {"dir": relative, "id": name}
        for relative, (name, _) in sorted(manifests.items())
        if relative not in bundles
    ]

    return {
        "source": {
            "repository": UPSTREAM_REPOSITORY,
            "revision": UPSTREAM_REV,
            "revisionShort": UPSTREAM_REV_SHORT,
            "version": UPSTREAM_VERSION,
            "derivation": (
                "internal/dshmount/roster.json is generated by scripts/gen-dsh-roster.py from "
                "the pinned upstream revision. For every staged "
                "webui/dsh/plugins/<dir>/client.js, <dir> is resolved to "
                "packages/<dir>/package.json and the entry id/inject/immediately are copied "
                "from dsh.client (platform=web), mirroring "
                "packages/client/modules/src/index.ts resolveMeta/graphRow. external is scanned "
                "from the shipped bundle bytes (real require() calls only, relative chunk "
                "requests dropped), so a rebuilt artifact re-derives it. shellProvided names "
                "the specifiers no graph row provides and the staged asset that seeds each. "
                "The per-entry rev is derived at request time from the staged client.js bytes."
            ),
        },
        "shellProvided": dict(sorted(shell_provided.items())),
        "entries": entries,
        "blocked": blocked,
        "omitted": omitted,
    }


def main() -> int:
    repo_root = pathlib.Path(__file__).resolve().parent.parent
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--src", default="/tmp/dsh-src", type=pathlib.Path)
    parser.add_argument("--stage", default=repo_root / "webui" / "dsh", type=pathlib.Path)
    parser.add_argument(
        "--out", default=repo_root / "internal" / "dshmount" / "roster.json", type=pathlib.Path
    )
    args = parser.parse_args()

    roster = build_roster(args.src, args.stage)
    rendered = json.dumps(roster, indent=2, sort_keys=False) + "\n"
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(rendered, encoding="utf-8")
    external_sizes = [len(entry.get("external", [])) for entry in roster["entries"]]
    sys.stderr.write(
        f"gen-dsh-roster: wrote {args.out} "
        f"({len(roster['entries'])} staged, {len(roster['blocked'])} blocked, "
        f"{len(roster['omitted'])} omitted, {len(roster['shellProvided'])} shell-provided; "
        f"external per entry total={sum(external_sizes)} max={max(external_sizes, default=0)})\n"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())