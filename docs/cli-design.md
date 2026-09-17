# CLI Design

ZenForge CLI is the fastest way to feel the runtime.

## Commands

```text
zenforge run
zenforge exec
zenforge code
zenforge resume
zenforge events
zenforge runs
zenforge init
zenforge version
```

## Tools

The default tool set is `workspace_read`, `workspace_list`, `workspace_glob`,
`workspace_grep`, `workspace_write`, `workspace_edit`, `apply_patch` (codex
envelope: add, update, move, delete in one call), `shell` (unless `--no-shell`),
`todo` tools, `ask_user`, `get_context_remaining`, and `present`. `tool_search`
is added only when at least one configured tool defers its definition, and
`web_search`/`web_fetch` only when `web.enabled` is true or
`web.searchEndpoint` is set (a search endpoint also registers
`web_search`).

## Sandboxes

`--sandbox <none|seatbelt|bwrap|docker|landlock>` (or
`shell.sandbox.backend`)
confines the shell in an OS-level sandbox: the backend is registered on the
shell tool, the session stays open so later calls reuse the layout, and a
denied command can be escalated per call through the existing approval
flow. `--sandbox-root` (repeatable) sets the writable roots — the shell
working directory by default — `--sandbox-allow-network` grants network
access, `--sandbox-protected` selects the basenames pinned read-only
inside writable roots, `--sandbox-restricted` switches bubblewrap from a
read-only host root to an empty root plus approved read roots,
`--sandbox-image` selects the container image, and `--sandbox-timeout`
bounds one sandboxed command (defaulting to `shell.timeout`). An unknown
backend is a usage error, and a backend that is unavailable on the host
fails the command with `sandbox_unavailable` rather than running
unsandboxed. The `landlock` backend confines the filesystem with Landlock
and the network with seccomp; because both restrictions must be installed
by the process that execs, it runs each command through a hidden
`zenforge linux-sandbox` helper that applies both layers and execs the
command.

The shell tool can escalate to a registered `sandbox.Sandbox`. Besides the
Docker and container-hub backends, `sandbox/bwrap` runs commands on Linux
under bubblewrap: the policy is built as an argument list (read-only or
tmpfs root, approved read roots, writable roots bound shallowest-first,
read-only rebinds for `.git` and explicit paths, user/pid/ipc namespaces,
all capabilities dropped, no network unless requested, fresh `/proc`), so
it is auditable and testable off Linux. Missing writable roots are dropped
and missing protected paths are skipped (bubblewrap cannot bind a missing
target), and no seccomp filter is applied yet. `sandbox/seatbelt` runs
commands under macOS Seatbelt: each session gets a generated SBPL profile that is closed by
default, grants write access only to the declared roots (pinning `.git` and
`.zenforge` read-only inside them), allows reads of the writable roots and
their ancestor chain, and denies network access unless the configuration
opts in. Paths are canonicalized and passed as `-D` parameters, and off
macOS — or when `sandbox-exec` is missing — opening a session fails with
`sandbox_unavailable` rather than running unsandboxed.

## Goals and Ralph

`--goals` (or `agent.goals`) registers `create_goal`, `get_goal`, and
`update_goal`. The lifecycle lives in the `goals` store (per session, under
`<checkpointDir>/goals`), so a goal survives a resume: revision-checked
transitions, a round budget, and the rule that a goal may only be reported
blocked after the same condition has persisted for three consecutive
rounds.

`zenforge goal "<objective>"` creates one goal and drives it round by
round, resuming the same run so the model keeps its conversation; the loop
stops as soon as the stored goal is complete or blocked. `zenforge ralph
"<objective>" --rounds N` instead runs a fresh agent per round over the
shared workspace: only the previous round's bounded structured report
crosses over, each report is validated and written to
`<checkpointDir>/ralph/<loop>/round-N.json`, and the loop stops on
`complete`, `blocked`, or the round limit.

## Plan mode

`--plan` (or `agent.planMode`) starts a run in the planning phase: mutating
tools (write, edit, apply_patch, shell, subagents) are refused with a
structured `PLAN_MODE_READ_ONLY` result while read tools, todo
bookkeeping, `ask_user`, `present`, `get_context_remaining`,
`tool_search`, and the web tools stay available. The model finishes by
calling `exit_plan_mode` with the plan; the CLI's approval prompt shows the
plan and, on approval, the run switches to executing durably (the phase is
part of run state, so a resume keeps it). With `--approve always` the first
plan is approved automatically, which is the sensible unattended default.

## Time travel

`zenforge fork <parent-run-id> [--at <seq>]` starts a new run from the
parent's newest checkpoint at or below `--at` (the latest checkpoint when
`--at` is omitted or 0). The child's conversation, todos, and tool state
come from the parent; its event log begins with its own `run.started`
carrying `forkedFrom`, its state records `parentRunId`, and the parent log
is untouched. The command prints `forked <parent> into <child>` on stderr
and then streams the continued run.

`zenforge revert --to <seq> <run-id>` rewinds a stored run without running
it: it appends a `run.reverted` marker and saves the state at the newest
checkpoint at or below `--to` as the run's newest checkpoint. History is
never truncated, so the abandoned branch stays in the log. It needs only
the checkpoint and event stores — no model or API key — and prints the
marker sequence on stdout.

`zenforge resume --revert-to <seq> <run-id>` performs the same rewind and
then resumes, which is the shorthand for "undo the last few steps and
continue".

## Configuration layers

Configuration is composed from ordered layers (system, user, profile, project,
`--config`, flags) with managed requirements applied last; see
[`config-reference.md`](config-reference.md) for the precedence table and the
`allowed`/`enforce` document. The flags that shape layering are:

- `--profile <name>` selects a `profiles.<name>` fragment (unknown names list
  what is available).
- `--requirements <file>` applies a managed constraints document; the
  host-wide `/etc/zenforge/requirements.json` is used when present.
- `--strict-config` rejects fields this version does not recognize, naming the
  layer that set them.
- `--ignore-user-config` skips the system and user layers.

## `zenforge exec`

`exec` is the headless entry point for scripts and editors. It shares every
option with `run` and adds machine output:

```bash
# JSONL event stream on stdout
zenforge exec --json "Summarize the failing tests"

# Constrain the final response to a JSON Schema and keep the last message
zenforge exec --output-schema answer.schema.json -o answer.json "Extract the risks"

# Read the prompt from stdin
echo "Summarize this diff" | zenforge exec -
```

- `--json` prints one `zenforge.Event` JSON object per line, the same record
  `zenforge events --json` prints.
- `--output-schema <file>` requires a non-empty JSON object; the schema is
  forwarded to the provider as a strict `json_schema` response format. A
  provider that cannot enforce a schema (Anthropic Messages API) fails with
  `model.ErrUnsupportedOutputSchema` instead of returning unconstrained text.
- `-o`, `--output-last-message <file>` writes the final assistant message.
- The prompt comes from the arguments, from `-`, or from piped stdin (1 MiB
  cap). Missing or blank input is a usage error (`exit 2`).

## `zenforge run`

```bash
zenforge run "Analyze this repo"
```

Initial implementation:

```bash
OPENAI_API_KEY=... zenforge run \
  --workspace . \
  --checkpoint-dir .zenforge/runs \
  --mode plan_execute \
  --approve prompt \
  "Analyze this repo"
```

`--mode react|oneshot|plan_execute` is the primary execution-preset flag.
`--planning` remains for compatibility with earlier ZenForge configs; callers
must not pass both flags in one command.

Behavior:

- loads config;
- creates run ID;
- starts event stream;
- renders model deltas;
- renders tool calls compactly;
- renders todos;
- prompts for approval if configured;
- writes checkpoints and event log.

## `zenforge code`

```bash
zenforge code ./repo "Analyze failures and implement the fix"
```

`code` uses the same model, mode, approval, tool, event, and checkpoint
assembly as `run`. Its first positional argument is authoritative: ZenForge
resolves it to a real directory and uses that directory as both the local
workspace root and shell working directory. A configured or flagged
`workspace.root` cannot redirect this command away from the selected
repository. Remaining positional arguments form the task input.

## `zenforge resume`

```bash
zenforge resume run_123
```

Initial implementation uses the same model/tool assembly flags as `run` and
loads checkpoint state from `--checkpoint-dir`.

Behavior:

- loads checkpoint;
- prints resume phase;
- continues if phase is supported;
- fails clearly if unsupported.

## `zenforge events`

```bash
zenforge events run_123
```

Initial implementation reads JSONL events from `--checkpoint-dir` and can print
compact timeline text or raw JSON with `--json`.

Behavior:

- reads event log;
- prints compact timeline;
- optional JSON output later.

## `zenforge runs`

```bash
zenforge runs
```

Reads latest checkpoints from `--checkpoint-dir` and prints run ID, phase,
status, step, and save time. Use `--json` for machine-readable summaries.

Behavior:

- lists local durable runs;
- sorts newest checkpoint first;
- skips directories without a valid latest checkpoint.

## `zenforge init`

```bash
zenforge init
```

Creates:

```text
zenforge.json
.zenforge/
```

The generated config is JSON. YAML config files are not accepted.

## Rendering

Tool call:

```text
tool workspace_grep {"pattern":"TODO","path":"."}
```

Todo update:

```text
todos
  [done] Inspect project structure
  [in_progress] Review tool runtime
  [pending] Draft plan
```

Approval:

```text
Approval required: shell command
Risk: high
Command: rm -rf build

1. Reject
2. Approve once
3. Approve for this run
```

## Exit Codes

- `0`: success;
- `1`: runtime error;
- `2`: invalid config/usage;
- `3`: run cancelled;
- `4`: approval rejected;
- `5`: unsupported resume state.
