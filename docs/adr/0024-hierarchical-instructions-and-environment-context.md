# ADR 0024: Project Instructions And Environment Context Are Discovered Once And Frozen In Run State

Status: accepted

## Context

Coding agents need two kinds of prompt context beyond the host's own
instructions: project rules that travel with the repository (codex's
hierarchical AGENTS.md merging, DSH's agent-instructions) and stable facts
about where and how the run executes (codex's `<environment_context>` block,
DSH's time context). Without them, every embedder hand-assembles prompts and
models guess at working directory, platform, or repository conventions.

ZenForge's resume contract makes this harder than it looks: run state is the
single source of truth, and a resumed run must replay prompt-identical
requests. Re-reading instruction files at resume time would let on-disk edits
silently change the prompting of an in-flight task, breaking reproducibility
and confusing models mid-run. Codex solves the same problem by persisting
`base_instructions` and the environment context in its rollout session
metadata.

Ecosystem compatibility also matters: repositories already ship AGENTS.md
and CLAUDE.md files for other agents, and ZenForge should read them with the
same precedence semantics rather than invent a parallel convention.

## Decision

### The instructions package discovers, the agent freezes

`instructions.Discover` walks from the project root down to the working
directory and merges instruction files, broad-to-specific:

- root markers (default `.git`) locate the project root; with no marker
  found, only the working directory itself is searched, and an explicitly
  empty marker list disables upward traversal entirely;
- per directory, the first existing candidate wins, in precedence order
  `AGENTS.override.md`, `AGENTS.md`, `ZENFORGE.md`, `CLAUDE.md` — the
  override file lets a local checkout replace shared rules without editing
  them;
- an optional user-global file (CLI default `~/.zenforge/AGENTS.md`) is the
  broadest scope; a missing global file is silently skipped;
- the merged budget (default 32 KiB) keeps files from most specific to
  broadest: once a file no longer fits, it and every broader file are dropped
  whole, and truncation on a rune edge happens only when the most specific
  file alone exceeds the budget — broader scopes lose before specific ones
  are damaged;
- individual source files are capped at 1 MiB; candidate names and markers
  containing path syntax are rejected as configuration errors before any
  filesystem probe; per-file read problems are fail-open warnings, while
  configuration problems fail closed.

`Render` produces the `# Project instructions` system block with explicit
precedence framing (more specific files take precedence; none of them
override system, developer, or direct user instructions), per-file sections,
and a warnings footer.

### The agent freezes both blocks into durable run state

On a fresh run, `applyRunContext` renders the `<environment_context>` block —
working directory, platform, UTC date, execution mode, and tool list, with
tag contents XML-escaped — and the rendered project instructions into
`RunState.Meta` under `zenforge.environment_context` and
`zenforge.project_instructions`, checkpointed before the first model call.
Resumed runs replay Meta and never rediscover: the codex
base-instructions-persistence analogue. Both blocks are injected as system
messages ahead of the conversation history, ordered after
`Config.Instructions` and before the skill catalog, and both feed the
compaction pressure estimate.

Discovery is observable: the `instructions.loaded` event reports
`projectRoot`, the merged `files`, rendered `bytes`, and `warnings`.
Discovery configuration errors fail the run (`run.error` with
`prepare run context`); a directory with no instruction files is simply
empty context, not an error.

Both features are library opt-in (`EnvironmentContext` bool,
`InstructionFiles` nil means off) and CLI default-on, with the workspace as
the discovery directory.

## Consequences

Benefits:

- resumed runs are prompt-identical to the original, preserving the
  checkpoint/resume replay contract;
- repository conventions travel with the repository and are compatible with
  the AGENTS.md ecosystem, including Claude- and codex-style files;
- the byte budget bounds prompt growth in deep directory trees, with
  precedence that protects the most specific rules;
- frozen environment facts stop models from guessing cwd, platform, or date,
  and the escaped tag grammar keeps injected content distinguishable from
  conversation;
- the durable event trail records exactly which files shaped the run, with
  warnings for everything skipped or truncated;
- discovery logic stays a standalone package with no agent coupling, so
  hosts can reuse it without the facade.

Costs:

- instruction edits mid-run require a new run to take effect — deliberate,
  but a behavior change users must learn;
- discovery does bounded filesystem work at run start;
- run-state Meta grows by up to the instruction budget plus the environment
  block per run;
- the frozen UTC date goes stale for runs resumed across day boundaries,
  which is the price of replay determinism.

## Alternatives Rejected

### Rediscover Instruction Files On Resume

On-disk edits would silently change the prompting of an in-flight task,
breaking resume determinism and the audit trail. Codex persists its base
instructions for the same reason.

### Let Hosts Assemble Instructions Through Config.Instructions

Hosts would each reimplement hierarchy, precedence, budgets, and warnings,
and the result would lack per-file provenance in events. `Config.Instructions`
remains the host's own system prompt and is injected ahead of the discovered
blocks.

### Let Instruction Files Override The System Prompt

The rendered framing states precedence explicitly: discovered files refine
behavior but never replace system, developer, or direct user instructions.
Treating repository content as top-authority would turn any checkout into a
prompt-injection vector against the host's own rules.
