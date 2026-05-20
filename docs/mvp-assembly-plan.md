# MVP Assembly Plan

This document turns S1-S8 into a shippable MVP plan.

The MVP should prove that ZenForge is a real Go agent harness, not just an
interface collection.

## MVP Promise

A developer can run:

```bash
zenforge run "Analyze this repository and propose a refactor plan"
```

and get:

- streamed progress;
- model output;
- visible tool calls;
- todo updates;
- safe workspace reads/grep;
- optional shell with approval;
- event log;
- checkpoints;
- final summary.

## MVP Must-Haves

### Runtime

- S1 event log;
- S1 checkpoint store;
- S4 minimal harness;
- max steps;
- cancellation;
- final no-tool answer turn.

### Model

- OpenAI-compatible adapter;
- fake model for tests;
- basic streaming text;
- tool-call support.

### Tools

- S2 tool registry/invoker/middleware;
- typed tool helper;
- S5 todo tools;
- S3 workspace read/list/grep;
- S3 workspace write with explicit write root;
- S3 shell tool with deny-by-default policy.

### Safety

- path jail;
- symlink escape blocking;
- output caps;
- shell timeout;
- command allowlist;
- approval-required result or CLI approval.

### CLI

- `zenforge run`;
- `zenforge resume`;
- config file support;
- stream event rendering;
- todo rendering;
- approval prompt.

### Examples

- `examples/repo-refactor-agent`;
- `examples/code-review-agent`;
- `examples/simple-tool-agent`.

## MVP Can Defer

- Container Hub adapter can be experimental or post-MVP.
- Sub-agent runtime can be behind experimental flag if time is tight.
- Full approval broker can start with CLI + always allow/deny.
- JSONL store is enough; SQLite can wait for V0.1.
- Anthropic adapter can wait.
- MCP can wait.
- Memory can wait.
- ZenMind adapter can wait until after standalone MVP.

## MVP Cut Line

Minimum MVP:

```text
S1 + S2 + S3 + S4 + S5 + basic S6 + CLI
```

Stretch MVP:

```text
basic S7 sub-agent + experimental S8 Container Hub adapter
```

Recommended MVP:

Do not block MVP on S7/S8. Ship those as experimental after the core
single-agent loop feels solid.

## CLI Commands

### `zenforge run`

```bash
zenforge run "Analyze this repository"
```

Options:

```text
--config zenforge.yml
--workspace .
--model gpt-4.1
--max-steps 20
--checkpoint-dir .zenforge/runs
--no-shell
--approve always|never|prompt
```

### `zenforge resume`

```bash
zenforge resume run_123
```

Options:

```text
--checkpoint-dir .zenforge/runs
--approve prompt
```

### `zenforge events`

Optional but useful:

```bash
zenforge events run_123
```

Prints event log.

## Config File

```yaml
model:
  provider: openai
  name: gpt-4.1
  apiKeyEnv: OPENAI_API_KEY

agent:
  instructions: |
    You are a senior Go backend engineer.
  maxSteps: 20
  planning: true

workspace:
  root: .
  read:
    - .
  write:
    - ./tmp
  maxReadBytes: 1000000
  maxWriteBytes: 1000000

shell:
  enabled: true
  workingDir: .
  allow:
    - go test ./...
    - go vet ./...
    - grep
    - find
  timeout: 30s
  maxOutputBytes: 256000

approval:
  mode: prompt

checkpoint:
  type: jsonl
  path: ./.zenforge/runs
```

## MVP Examples

### simple-tool-agent

Purpose:

- prove SDK tool helper;
- no filesystem needed.

Acceptance:

- fake or real model calls a custom tool;
- final answer includes tool result.

### repo-refactor-agent

Purpose:

- flagship MVP demo.

Uses:

- workspace read/list/grep;
- todo tools;
- optional shell.

Acceptance:

- creates todo list;
- inspects repo;
- writes no files by default;
- produces refactor plan.

### code-review-agent

Purpose:

- prove safety and repeated workflow.

Uses:

- grep;
- read;
- optional `go test ./...`.

Acceptance:

- reports findings;
- notes test gaps;
- streams tool calls.

## MVP Test Strategy

Unit tests:

- event log;
- checkpoint;
- tool runtime;
- workspace policy;
- shell policy;
- planner/todo;
- harness fake model loop.

Integration tests:

- fake model + fake tools;
- fake model + local workspace tools;
- fake model + todo planner;
- CLI with fake model provider;
- JSONL resume from pending tool.

Manual smoke:

- run CLI in ZenForge repo;
- run CLI in small Go repo;
- interrupt and resume;
- approval prompt for shell.

## MVP Acceptance Checklist

Runtime:

- [ ] `Agent.Stream` works with fake model.
- [ ] `Agent.Run` returns final output.
- [ ] OpenAI-compatible model can stream text.
- [ ] Model tool calls invoke tools.
- [ ] Checkpoints written at boundaries.
- [ ] Resume works for supported boundaries.

Tools:

- [ ] typed tool helper works.
- [ ] workspace read/list/grep works.
- [ ] workspace write respects roots.
- [ ] shell command allowlist works.
- [ ] risky shell returns approval request or prompt.

Planning:

- [ ] todo tools work.
- [ ] plan/execute preset works with fake model.
- [ ] todo updates stream.

CLI:

- [ ] `zenforge run` works.
- [ ] `zenforge resume` works.
- [ ] config file works.
- [ ] approval prompt works.

Docs:

- [ ] quickstart.
- [ ] config reference.
- [ ] tool authoring guide.
- [ ] security guide.
- [ ] limitations section.

## MVP Limitations To State Clearly

- Resume does not continue mid-token model streams.
- Resume does not continue an OS command already running at crash time.
- Shell is deny-by-default.
- Memory is not included.
- MCP is not included.
- Sub-agents may be experimental or absent from MVP.
- Container Hub may be experimental or absent from MVP.

## Release Candidate Criteria

Before tagging MVP:

- all tests pass;
- examples run from clean checkout;
- CLI has useful error messages;
- no imports from `agent-platform`;
- no hardcoded ZenMind paths;
- generated checkpoint/event files have documented schema version;
- README quickstart works.

