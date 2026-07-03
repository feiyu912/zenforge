# Agent Skills Spec

This stage adds portable, progressively disclosed instruction resources to
ZenForge without moving application catalog ownership into the harness.

An Agent Skill is a named instruction package whose metadata can be advertised
to a model and whose `SKILL.md` body can be loaded on demand. A skill may teach
the agent when and how to use tools, but it is not itself an executable tool.
In particular, an ordinary typed `tool.Tool` is not a skill. Tools provide
model-callable operations with schemas and runtime policy; skills provide
application-selected guidance and provenance.

## Outcome

After this stage, an application should be able to:

- assemble a skill catalog from explicit sources;
- validate and merge skill metadata deterministically;
- expose only allowed skill metadata in the initial model context;
- provide a bounded `load_skill` operation that returns validated `SKILL.md`
  content;
- identify loaded content by digest and provenance;
- keep tool, approval, sandbox, sub-agent, and checkpoint behavior under their
  existing owners;
- integrate a platform marketplace without placing marketplace behavior in
  ZenForge.

This document specifies the contracts. It does not make skill loading a harness
responsibility and does not define a ZenMind marketplace protocol.

## Ownership And Package Boundary

The catalog is application-owned. A CLI, SDK application, or platform adapter
chooses sources, trust policy, allowlists, and size limits, then constructs a
`skill.Bundle`. The high-level `zenforge.Agent` facade consumes that bundle
through `Config.Skills`.

The reusable parts may live in a small package such as `skill`, but package
code must remain host-driven:

```text
application / adapter
  choose sources and precedence
  load and validate catalog
  apply tenant/agent/run allowlist
  construct immutable skill.Bundle
          |
          v
zenforge.Config.Skills -> Agent facade
  add descriptor-only system message
  add bundle load_skill tool
          |
          v
normalized model/tool callbacks -> harness runner
```

The harness:

- does not scan directories;
- does not read `SKILL.md` files;
- does not contact registries or marketplaces;
- does not decide which skills a tenant or agent may use;
- does not inject every skill body into the prompt.

The lower-level harness runner receives callbacks and state only. It does not
inspect `Config.Skills` or perform filesystem discovery.

## Terminology

| Term | Meaning |
| --- | --- |
| skill package | A directory containing one validated `SKILL.md` entry point and optional application-managed resources. |
| skill metadata | Bounded `name` and `description` fields advertised before loading the body. |
| skill body | Markdown after the front matter in `SKILL.md`. |
| source | An application-configured catalog input, normally a directory or an already materialized package set. |
| catalog | A read-only source of descriptors and complete skill content. Filesystem catalogs rescan their configured root on each operation. |
| bundle | The immutable, allowlisted in-memory snapshot used by an agent. |
| load | Returning one snapshotted skill body and its digest/provenance through `load_skill`. |

Availability is not authority. Seeing or loading a skill does not grant a tool,
filesystem root, command, sandbox backend, sub-agent, credential, or approval.

## Application-Facing Contract

The implemented Go contract is:

```go
type Descriptor struct {
    Name          string
    Description   string
    License       string
    Compatibility string
    Metadata      map[string]any
}

type Provenance struct {
    Source string
    Path   string
}

type Content struct {
    Descriptor Descriptor
    Body       string
    Digest     string
    Provenance Provenance
}

type Catalog interface {
    List(ctx context.Context) ([]Descriptor, error)
    Load(ctx context.Context, name string) (Content, error)
}
```

`Catalog` exposes read-only operations. Implementations return copies or
immutable values. `Load` accepts a canonical name, not a path.

`skill.NewBundle(ctx, catalog, allowlist, options...)` lists and validates the
catalog, applies the allowlist, loads every selected entry, verifies that its
loaded descriptor still matches the listed descriptor, and freezes the
resulting prompt, content, and fingerprint in memory. Bundle construction,
rather than the harness runner, is the fail-closed startup boundary.

## `SKILL.md` Format

Each package has exactly one entry point named `SKILL.md` at its package root.
The file is UTF-8 Markdown with YAML front matter:

```markdown
---
name: code-review
description: Review a code change for correctness, regressions, and missing tests.
---

# Code Review

Use this skill when the user asks for a code review.
```

Required front matter fields:

- `name`: canonical catalog identity;
- `description`: concise discovery text explaining purpose and when to load it.

The filesystem parser also accepts optional scalar `license` and
`compatibility` fields and a flat string-valued `metadata` mapping. Other
front matter fields, duplicate keys, multiline descriptions, nested values
outside `metadata`, and non-mapping `metadata` are rejected.

The body may contain instructions and relative references for the agent, but
loading version 1 returns only the validated `SKILL.md` body. Referenced files
are not recursively expanded or read implicitly. Future resource-loading APIs
require their own path, size, policy, and provenance contract.

The body is untrusted model context. It cannot configure runtime policy through
front matter or prose.

## Validation Limits

Defaults are intentionally conservative and application-configurable only
toward stricter limits:

| Property | Required rule |
| --- | --- |
| canonical name | 1-64 ASCII bytes; lowercase `a-z`, digits, and single `-` separators; regex `^[a-z0-9]+(?:-[a-z0-9]+)*$` |
| description | 1-512 UTF-8 bytes after trimming; one logical paragraph; no control characters |
| `SKILL.md` | at most 64 KiB |
| front matter | at most 8 KiB and must terminate within the file limit |
| catalog entries | at most 256 before run filtering (`MaxCatalogEntries`) |
| advertised entries | at most 64 after run filtering (`MaxAdvertisedEntries`) |
| total advertised metadata | at most 32 KiB (`MaxAdvertisedMetadataBytes`) |
| loaded body | at most 64 KiB, with no truncation |

Names are compared exactly after validation. Unicode lookalikes, underscores,
dots, slashes, leading/trailing hyphens, and case folding are not accepted.
Descriptions are used as parsed; validation rejects empty descriptions,
newlines, and control characters.

An oversized document fails validation or load with a typed error. It is never
silently truncated because truncation changes instructions while obscuring
their digest.

All limits in the table are hard library defaults. A non-zero option may only
select a positive value less than or equal to its default; attempts to raise a
default are rejected as invalid configuration. A zero option selects the
default.

The filesystem catalog counts direct child packages that contain a
`SKILL.md`. Discovery fails closed when that count exceeds
`MaxCatalogEntries`; it never returns a truncated catalog. A directly
configured single-package directory counts as one entry.

`NewBundle` applies the allowlist and then checks `MaxAdvertisedEntries`.
Advertised metadata bytes are the UTF-8 byte length of the exact deterministic
`CatalogPrompt()` string: the `Available skills:\n` header followed, in
canonical-name order, by `- <name>: <description>\n` for each filtered entry.
The fixed header, punctuation, spaces, and newline bytes all count. License,
compatibility, arbitrary front matter metadata, skill bodies, fingerprints,
and tool schemas do not count because they are not in that prompt. The exact
entry and byte limits are accepted; one over either limit fails before any
skill body is snapshotted.

## Path Safety

Directory-backed sources must enforce a root jail independently of workspace
tool policy:

- source roots are explicit and absolute after configuration resolution;
- package discovery considers direct configured package paths or immediate
  child directories only; it does not walk arbitrary trees by default;
- the entry point is the literal basename `SKILL.md`;
- clean and evaluated paths must remain beneath the source root and package
  root;
- `..`, absolute user input, NUL bytes, alternate separators, and path-shaped
  skill names are rejected;
- symlinked source roots, package directories, and entry points are rejected by
  default; an application may materialize trusted packages into a safe root
  before catalog construction;
- entry points must be regular, non-symlink files, not devices, sockets, or
  FIFOs;
- reads are bounded and verify that the opened file is the same file previously
  inspected;
- `load_skill` performs lookup by canonical name and never joins model-provided
  text into a filesystem path.

A workspace may happen to contain skill files, but the workspace is not an
implicit catalog source. The application must opt in to a source and assign its
trust and precedence.

The filesystem implementation does not inspect platform-specific link counts,
so it cannot reliably distinguish a regular file from a hard-link alias on all
supported Go platforms. Applications must not use an untrusted writable source
when hard-link aliasing is in scope; they should first materialize packages
into an application-controlled directory. The regular-file, symlink, root-jail,
bounded-read, and opened-file identity checks still apply.

## Source Layering

Applications pass an ordered list of sources from lowest to highest
precedence. A recommended standalone order is:

```text
1. application-bundled
2. administrator-configured
3. workspace-configured
4. run-injected
```

These labels are descriptive, not mandatory source types. ZenMind may map
platform records to materialized sources before invoking this layer.

Merge rules:

1. validate every source and every candidate independently;
2. for filesystem catalogs, require the package directory basename to equal
   the canonical `name` in front matter;
3. pass catalogs to `skill.Merge` from lowest to highest precedence;
4. when names collide, a later catalog replaces the earlier descriptor and is
   searched first by `Load`;
5. do not emit shadowing or collision diagnostics; explicit shadowing policy is
   not part of the `skill.Merge` API;
6. sort the final catalog by canonical name, independent of filesystem order.

Each selected entry is loaded atomically from one catalog. An allowlist is
evaluated after layering against the selected name.

Remote discovery, download, signature verification, updates, ratings, billing,
and publication are outside this contract. A host must materialize and verify
remote packages before presenting them as a source.

## Progressive Disclosure

Initial prompt construction exposes metadata only. A compact deterministic
section may contain:

```text
Available skills:
- code-review: Review a code change for correctness, regressions, and missing tests.
- release-check: Prepare and verify a repository release.
```

The initial model request must not include skill bodies, hidden package paths,
source credentials, marketplace metadata, or entries outside the run allowlist.

When `Config.Skills` is non-nil, the `zenforge.Agent` facade appends the
bundle's descriptor-only `CatalogPrompt()` as a system message and registers
the bundle's `load_skill` tool. A caller-supplied tool whose normalized name is
`load_skill` is a configuration error:

```json
{
  "name": "code-review"
}
```

Its schema accepts exactly one canonical `name`; additional properties and
trailing JSON are rejected. On success the tool's text output is the body and
its structured metadata is:

```json
{
  "name": "code-review",
  "digest": "sha256:...",
  "provenance": {
    "source": "app-bundled",
    "path": "code-review/SKILL.md"
  }
}
```

The result is ordinary tool-result context and follows existing output limits,
events, and checkpoint boundaries. Repeated loads of the same run-view entry
must return identical content and digest. Unknown, denied, or invalid names
return the same `skill unavailable` result without revealing whether a hidden
catalog entry exists.

## Allowlists

`NewBundle` accepts `[]string` canonical names. A nil allowlist permits every
discovered skill; a non-nil empty allowlist permits none. Names are exact,
duplicates are ignored, and an unknown allowlisted name makes construction
fail with `ErrNotFound`. Digest pins, globs, regexes, and policy intersection
are application concerns and are not represented by this API.

The same frozen bundle drives both prompt metadata and `load_skill`, so later
catalog or allowlist changes do not mutate an existing agent's view.

## Digest And Provenance

Every selected entry has a lowercase SHA-256 digest represented as
`sha256:<64 hex characters>`. The filesystem catalog computes it over the
original complete `SKILL.md` bytes, including front matter, with no newline,
BOM, Unicode, or Markdown normalization.

Provenance contains an application-assigned `Source` and a safe,
source-relative slash-separated `Path`. It does not expose the absolute host
root.

The filesystem catalog rescans and rereads on `List` and `Load`.
`NewBundle` performs both operations during construction and then serves
`load_skill` from its frozen in-memory copy. Consequently, disk changes after
bundle construction do not alter or invalidate that bundle.

Marketplace signatures or package digests may be retained as additional
provenance, but they do not replace the content digest used here.

## Runtime Boundaries

### Tools

`load_skill` is a bundle-provided read-only typed tool registered by the Agent
facade as a
transport for progressive disclosure. That does not make typed tools skills,
nor does it make skills executable tools.

- tool schemas describe callable operations;
- skill descriptions describe instruction resources;
- loading prose does not register new tools;
- mentioning a tool name in a skill does not bypass the run's tool registry;
- unknown or unavailable tools remain unavailable.

### Approval And HITL

Loading an already allowlisted, local, immutable body is normally low-risk and
need not request approval. Approval remains mandatory wherever existing policy
requires it for subsequent tool operations. Skill text cannot approve an
operation, create an approval grant, choose a decision scope, or suppress HITL.

Fetching, installing, updating, or trusting a skill is a separate
application/platform action and may require approval before catalog
construction. It is not performed by `load_skill`.

### Sandbox And Workspace

`load_skill` reads from the bundle's frozen in-memory snapshot, not through
model-selected workspace paths and not by shelling out. A loaded skill does not
expand workspace roots or sandbox mounts. Commands and file access described
by the body still pass through tool policy and the configured sandbox adapter.

### Sub-Agents

Each child run receives an independently computed run view. The parent does not
implicitly pass loaded bodies or its full allowlist to children. The host may
configure a child allowlist that is an equal or narrower subset of policy
available to the parent/application.

The `task` runtime tool remains sub-agent orchestration, not a skill. A skill
may recommend delegation, but it cannot enable nested sub-agents, alter depth or
task limits, select unregistered agents, or grant child tools.

### Checkpoint And Resume

Skill source files and complete catalogs are not copied into checkpoints.
Checkpoints store the bundle fingerprint in internal run metadata. The
fingerprint covers the sorted selected descriptors, content digests, and
provenance, plus an independently computed SHA-256 hash of each frozen body.
Resume therefore still rejects a body change if a custom catalog accidentally
reuses a declared digest. A missing, newly added, removed, or different bundle
is rejected before model or tool execution. Normal tool call/result state
records any disclosure already in progress or completed.

Normal `load_skill` calls use existing before-tool and after-tool checkpoint
boundaries. No new approval, subtask, or sandbox checkpoint state is implied.

## ZenMind Boundary

ZenMind owns its skill marketplace and platform concerns, including:

- discovery, search, ranking, ratings, and recommendations;
- publishing, moderation, installation, update, and removal;
- tenancy, authentication, authorization, billing, and entitlements;
- package signatures, malware review, remote retrieval, and cache lifecycle;
- platform UI, APIs, audit records, and marketplace compatibility.

The ZenMind adapter may resolve entitled marketplace packages, materialize them
into trusted immutable inputs, map platform policy to an allowlist, and pass
the resulting metadata/body catalog into the application-owned contract.
ZenForge must not import marketplace DTOs or call marketplace services from the
harness.

## Errors And Observability

The package exposes sentinel errors:

```text
ErrNotFound
ErrNotAllowed
ErrUnavailable
ErrInvalid
ErrTooLarge
ErrPathEscape
```

Public events may continue to use `tool.call`, `tool.result`, and `tool.error`
for `load_skill`. Trace/audit attributes should include canonical name, digest,
safe source ID, outcome, and size, but never the body by default. Event and
trace projections must honor existing redaction and output-size policy.

## Acceptance Test Matrix

| Area | Acceptance case | Expected result |
| --- | --- | --- |
| format | valid supported front matter and Markdown body | entry is listed and loads |
| format | missing delimiter/name/description, duplicate key, unknown key, or unsupported multiline value | catalog listing fails with `ErrInvalid` |
| name | boundary-valid 1/64-byte names | accepted |
| name | uppercase, Unicode lookalike, slash, dot, underscore, repeated/edge hyphen, 65 bytes | rejected |
| description | empty, control character, multiline paragraph, or over 512 bytes | rejected |
| size | front matter, file, catalog, advertised count, and metadata total at each limit | exact limit accepted; one over rejected |
| path | symlinked root/package/entry point or special entry point | rejected with no bytes disclosed; hard-link aliases require an application-controlled source |
| lookup | model passes path or extra JSON property to `load_skill` | schema/validation failure before file access |
| layering | unique names across ordered sources | deterministic sorted catalog |
| name | filesystem directory basename differs from front matter name | rejected |
| layering | duplicate across ordered catalogs | later catalog wins for listing and loading |
| allowlist | listed and loaded views use the same exact-name intersection | no metadata/body discrepancy |
| allowlist | unknown and known-but-denied names | same non-enumerating error shape |
| disclosure | initial model request captured through `zenforge.Agent` | contains allowed name/description only, no body/path, and includes `load_skill` |
| disclosure | successful `load_skill` | returns one bounded body, digest, and safe provenance |
| disclosure | repeated load in one run | byte-identical result, no catalog mutation |
| integrity | filesystem content bytes | digest equals SHA-256 of the complete original `SKILL.md` |
| integrity | source changes after bundle construction | frozen body and digest remain unchanged |
| tools | skill mentions absent or approval-required tool | tool remains absent or follows normal approval flow |
| HITL | body claims an operation is approved | no grant is created; policy still requests approval |
| sandbox | body requests shell/path outside configured boundary | existing sandbox/workspace policy denies it |
| sub-agent | parent loads skill; child has no matching allowlist | child prompt and loader cannot see it |
| sub-agent | body asks for nested delegation above host limit | runtime rejects through existing guard |
| checkpoint | load crosses normal tool boundaries | pending/result state resumes without duplicate disclosure |
| checkpoint | resume with matching bundle fingerprint | continues deterministically |
| checkpoint | resume with added, removed, or changed bundle | fails closed before model/tool execution |
| observability | success and failure traces | include name/digest/source/outcome; omit body and absolute path |
| ZenMind | adapter supplies materialized entitled package | core uses catalog contract without marketplace DTOs/network |
| boundary | register an ordinary typed tool only | it is not listed as a skill and cannot be loaded |

End-to-end acceptance must include a fake model that first sees metadata, calls
`load_skill`, receives the body, and then invokes an allowed tool. Assertions
must prove that disclosure does not alter tool registry, approval decisions,
sandbox policy, sub-agent limits, or checkpoint ordering.
