# ADR 0022: Agent Skills Use Application-Owned Progressive Disclosure

Status: accepted

## Context

ZenForge needs Agent Skills that can guide a model with reusable instructions
without making every skill body part of every model request. The existing
architecture assigns catalog loading and `skill.Bundle` construction to the
application or adapter. The high-level Agent facade translates `Config.Skills`
into normalized model messages and tools; the harness runner receives callbacks
and state and does not inspect skill files.

There is also a terminology risk. ZenForge has ordinary typed tools and special
runtime tools such as `task`. A skill can explain how to use those capabilities,
but an ordinary typed tool is not thereby a skill. Treating the two concepts as
synonyms would collapse discovery content, executable authority, middleware,
and approval policy into one unsafe abstraction.

ZenMind has broader platform needs such as a marketplace, tenancy, entitlement,
installation, and moderation. Those concerns must not become dependencies of
the standalone harness.

## Decision

Agent Skill catalogs are application-owned. Applications and adapters choose
ordered sources, validate and layer packages, apply allowlists, and construct
an immutable `skill.Bundle` that binds content digests and provenance.

A skill package has a validated `SKILL.md` with required `name` and
`description` front matter plus a Markdown body. The initial prompt contains
only allowlisted metadata. When `Config.Skills` is configured, the
`zenforge.Agent` facade automatically adds that descriptor-only system message
and the bundle's `load_skill` tool. The body is disclosed only when the model
calls that tool.

The run-visible catalog is immutable and fail-closed:

- names, descriptions, sizes, and paths have bounded validation;
- ordered catalogs use deterministic later-catalog-wins precedence;
- prompt listing and loading use the same exact-name allowlist;
- selected content is frozen in the bundle with a SHA-256 digest and safe
  provenance;
- resume verifies the bundle fingerprint rather than silently changing views.

`load_skill` is a read-only typed tool used to carry disclosure through the
existing model/tool protocol. This implementation detail does not redefine
skills as tools. Loading instructions grants no executable capability.

Existing owners retain authority:

- the tool registry decides which operations exist;
- tool middleware and HITL decide whether operations require approval;
- workspace and sandbox policy decide where operations execute;
- sub-agent configuration decides delegation, child tools, and child skill
  views;
- checkpoint state records disclosed identity and normal tool progress.

The harness runner does not scan skill directories, read package files, merge
sources, resolve marketplace packages, or decide tenant/agent allowlists.

ZenMind continues to own marketplace discovery, distribution, signatures,
moderation, tenancy, entitlements, updates, and UI/API behavior. Its adapter may
materialize authorized packages and translate them into this application-owned
catalog contract.

The normative validation, disclosure, integrity, boundary, and acceptance
requirements are defined in `docs/agent-skills-spec.md`.

## Consequences

Benefits:

- prompt cost scales with metadata plus skills actually loaded;
- unselected instruction bodies are not exposed to the model;
- catalog and marketplace policy remain replaceable host concerns;
- fingerprint-bound bundles make active runs and resume deterministic;
- skills cannot bypass tool, approval, sandbox, or sub-agent controls;
- standalone SDK users do not inherit ZenMind platform dependencies.

Costs:

- applications must assemble a catalog and construct a bundle;
- adapters must preserve allowlist, provenance, and digest identity;
- changing the configured bundle causes fail-closed resume instead of
  transparent upgrade;
- source validation and snapshotting add startup work;
- models spend a tool call when they need a skill body.

## Alternatives Rejected

### Inject Every Skill Body Into The Initial Prompt

This wastes context, exposes irrelevant instructions, and increases prompt
conflict and injection surface. It also makes catalog growth directly increase
every request.

### Let The Harness Discover Catalogs

This would violate the normalized-input boundary and force filesystem,
tenancy, marketplace, and host policy into the reusable state machine.

### Treat Every Typed Tool As A Skill

A tool is executable behavior with a JSON schema and middleware. A skill is
instruction content selected through metadata. Equating them would make tool
registration imply prompt content and could make loading prose appear to grant
authority. The concepts remain separate even though `load_skill` itself uses
the tool transport.

### Let `load_skill` Accept A Path Or Fetch A Remote Package

Model-selected paths and network locations bypass catalog allowlists and make
integrity, sandboxing, and provenance ambiguous. Version 1 loads only a
canonical name from an already materialized immutable run view.

### Put The ZenMind Marketplace In ZenForge Core

Marketplace lifecycle and policy are platform concerns. Importing them would
couple the SDK to ZenMind DTOs, services, authentication, and release cadence.
