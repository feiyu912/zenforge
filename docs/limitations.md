# Limitations

ZenForge is an MVP harness. It is intentionally explicit about what is durable,
what is experimental, and what remains adapter territory.

## Runtime

- Resume replaces an interrupted model attempt from the committed prompt
  boundary; it does not continue through a provider-native mid-token cursor.
- Tool argument event redaction does not remove original arguments from durable
  checkpoints because resume needs them.
- Resume does not assume an OS command completed if the process crashed while
  the command was running.
- Resume is strongest at checkpoint boundaries: before model calls, after model
  calls, before tools, after tools, and around approval waits.
- Long-running command cancellation depends on the configured shell or sandbox
  backend.

## HTTP Lifecycle

- Detached `RunManager` ownership, status retention, duplicate exclusion, and
  active-run accounting are process-local unless `RunManagerOptions.Registry`
  is configured. `NewMemoryRunRegistry` and `OpenSQLiteRunRegistry` provide the
  supported registry implementations.
- Registry leases fence start/resume ownership and preserve durable status/list
  snapshots. Another manager can attach by replaying and polling the shared
  event store, but the live event bus is still process-local. Multi-replica
  deployments still need deliberate reconnect routing, provider/tool
  side-effect idempotency, and application-owned shutdown policy. Durable
  approval inboxes make approval list/submit shareable; they do not by
  themselves move execution between workers.
- Status, list, durable attach, and durable approval operations may use any
  correctly configured replica. Cancellation may also use any replica when the
  registry implements `RunCancellationRegistry`; the built-in memory and
  SQLite registries do. Custom registries without that optional interface must
  route cancellation using trusted `RunInfo.OwnerID`. Lease expiry permits
  explicit resume but does not automatically transfer execution. A resume
  owner consumes an inherited cancellation before opening the agent stream.
- `RunManager.RecoverStale` is an application-triggered scan, not an automatic
  controller. It reports per-run failures and still requires shared durable
  checkpoints/events plus a listing registry.
- Attachment disconnect stops only replay/follow delivery. It does not cancel
  detached execution; callers must use explicit cancel, a run timeout, or
  runtime shutdown.
- Terminal retention removes only manager status records. Durable events remain
  caller-owned and may still be replayed after status expires.
- The application owns OpenAI/Anthropic protocol and compatible base URL
  selection, credentials, auth/tenancy, route paths, durable stores and their
  closure, HTTP server shutdown, and `Runtime.Close`.

## Tools And Safety

- Shell is deny-by-default.
- Cross-run approval reuse is opt-in and limited to approved `ScopeRule`
  decisions. It requires a configured grant store, trusted tenant/subject
  namespace, and exact `ruleKey` plus operation `fingerprint`; once/run scopes
  remain checkpoint-only.
- Durable approval inboxes persist pending requests and committed decisions;
  they do not make external tool side effects exactly-once and they do not
  replace a distributed run lease.
- Workspace tools enforce local root boundaries, but they are not a replacement
  for OS sandboxing when running untrusted workloads.
- Sandbox support is adapter-based. Core works without Container Hub.
- Legacy sandbox checkpoint state without run/subtask ownership is reopened
  instead of reused.

## Planning And Sub-Agents

- Plan/execute is a preset, not a general project-management system.
- Sub-agents resume from explicit parent and child checkpoint boundaries; they
  do not resume an in-flight provider stream inside a child run.
- Nested sub-agents are blocked by default and remain outside the MVP surface.

## Workflows

- `workflow/` runs the reference's orchestration scripts in-process (ADR
  0057) and the `workflow` tool wires them to sub-agent runs wherever
  sub-agents are configured (ADR 0059). ZenForge's CLI does not configure
  sub-agents, so the tool is not advertised by `zenforge run` today; a host
  that does (the SDK, the ZenMind adapter) gets it next to the task tools.
- Workflow children are not registered in the parent's run-state subtask
  plan, so a resumed run replays the whole script instead of replaying
  children; children are reused only through their deterministic
  `<toolCallId>_agent_<n>` ids and existing child checkpoints.
- `agent(prompt, {schema})` asks the child for one JSON object, and an answer
  that is not JSON, not an object, or does not satisfy the schema is a failed
  item (`null`) with every violation written to the workflow's log (ADR 0065).
  Conformance beyond the enforced schema subset — `pattern`, numeric bounds,
  `format`, and anything else the subset refuses — is still the model's
  obligation, because a schema using those keywords is refused at the call.
- A conforming answer is the object; a violating one is not partly usable. A
  script that wants to inspect partial data has to omit the schema and parse
  the text itself.
- The engine's own progress is four first-class events (ADR 0066):
  `workflow.phase`, `workflow.log`, `workflow.agent.started`, and
  `workflow.agent.done`, each carrying `workflow`, `parentRunId` and
  `toolCallId` with the kind's fields flat (`phase`; `message`;
  `seq`/`label`/`phase`; `seq`/`label`/`phase`/`outcome`). A child run's
  lifecycle and streamed events stay on `subtask.*`, which is a change for a
  reader that used to filter `subtask.event` for a `type` of `workflow.*`.
- `workflow.agent.started`/`.done` and `subtask.started`/`.done` both describe
  a child, because they are two views: the script's bookkeeping (sequence,
  label, phase, outcome, including a child that never started) and the child
  run itself (child run id, status, error, its own stream). Neither replaces
  the other.
- A workflow is one JavaScript realm per run. Scripts cannot share state
  across runs, and the engine persists nothing itself.
- Scripts are bounded by `SyncTimeout`, `MaxConcurrentAgents`,
  `MaxTotalAgents`, `MaxItemsPerCall`, and `CancelGrace`; the caps are the
  reference's defaults and are per-engine configuration, not per-script
  arguments, so a script cannot raise its own limits.
- A child agent's provider or model override is resolved by the host
  (`Config.ModelResolver`; ADR 0064). The host's own provider keeps the host's
  configured credentials, while any other provider is read from its own
  environment variables — this configuration has one model section, so that is
  the only place a second provider's key can live. A name that cannot be
  resolved is a fatal `AGENT_START`, never a silent run on the host's model.

## Schedules

- A durable schedule is a file (`<checkpoint dir>/schedules.json`) plus
  whatever runs it. ZenForge does not keep a timer running: `schedule run-due`
  is meant to be invoked by cron, launchd, or a systemd timer, so nothing here
  holds a process open and a restart is just the next invocation.
- Windows that passed while nothing ran are counted as missed, not replayed.
  A schedule that was down for a weekend fires once, and `schedule list` shows
  how many windows were skipped; a scheduler that fired once per missed window
  would turn downtime into a burst of runs.
- Only `run-due` decides what is due, and it decides at the moment it starts:
  two overlapping timer invocations can both see the same schedule as due, so
  a timer should not run this concurrently with itself. The file is written by
  rename, so a reader never sees a half-written set.
- There is no signed-webhook trigger yet, and a schedule always runs as the
  operator who added it, in the workspace recorded with it.

## Served Runs

- A detached MCP run lives in the server process: its state is in an
  in-process registry capped at 100 terminal records (oldest evicted first; a
  live run is never forgotten), and the durable checkpoint store remains the
  long-term record. A server that exits takes the registry with it, so a
  detached run's own answer is available from `zenforge_run_status` only while
  that server is up; afterwards the durable summary is what remains.
- The registry is read by `zenforge_run_status`, which answers for a run this
  server started (live or terminal), then for a run this install recorded, and
  `unknown` for an id in neither. `unknown` is a result, not an error.
- A server cancels its live runs on the way out and waits a bounded time for
  them, so it does not exit while a detached run is still writing to its
  workspace. A detached run is still bounded by `--run-timeout`, and the
  caller that started it can stop it early with `zenforge_run_cancel` (same
  `--allow-run` grant). Cancellation is cooperative: it lands at the run's next
  cancellation point, so a tool call already in flight finishes or fails on its
  own terms before the run ends. A run stopped by its bound is reported as
  `cancelled`/`timeout` by what its own context says, not by the shape of the
  error the interruption happened to produce (a checkpoint load that is
  cancelled mid-flight returns a store error that does not chain to
  `context.Canceled`).

## Long-Running Commands

- A terminal job (`pty: true`) has one stream: its stderr view is always empty
  and its stdout carries both, and the terminal's own behaviour applies
  (`\n` becomes `\r\n`, typed input is echoed, full-screen escape sequences
  land in the output). Do not choose `pty` for output a program must parse.
- A terminal job's environment gets a `TERM` only when the resolved
  environment has none; an explicit host or spec environment is never
  overridden, so a caller that wants colours must set `TERM` itself.
- Terminal jobs are Unix-only: off Unix `pty: true` is refused with a clear
  error rather than run on pipes while claiming a terminal, and the
  process-group kill that stops a terminal session's children is Unix-only.
- A job's environment is never rendered (`Spec.Env` is `json:"-"`), which is
  why no credential scrubber exists: there is no snapshot surface to scrub.
  A host that starts recording job environments must add the scrubber with
  that surface (ADR 0060).
- The bounded buffer keeps a head of at most 8KiB (a quarter of the stream
  cap) plus the newest bytes, so a reader that never polls still loses the
  middle; it is told how much (`elidedBytes`).

## Persistent Approvals

- A standing "always allow this tool" decision is persisted only when
  `approval.grantsFile` names a file; it then covers that tool with any
  arguments, across runs, in the namespace it was granted for
  (`approval.tenant`/`approval.subject`, defaulting to `cli` and the
  operating-system user name). A once- or run-scoped decision never leaves the
  run it was made in (ADR 0062).
- The grants file is authority that outlives the process: it must be readable
  only by its operator, and its namespace is what keeps one operator's grant
  from answering for another's.
- `zenforge grants list` shows what a namespace holds and `zenforge grants
  revoke` takes one — or, with `--all`, everything — back. A bare rule key
  revokes the standing grant; `--fingerprint` selects a payload-pinned entry
  (ADR 0063).
- Revocation is consulted at decision time, so a call that already reused a
  grant stays approved and the next call sees the revocation. The listing
  shows the store, not the grants a running agent holds in run state.
- A grant TTL, tenant, or subject without a grants file is a configuration
  error rather than a silently ignored key.

## MCP Servers

- A server's `startupTimeout` covers `initialize` and `tools/list` together and
  defaults to 30 seconds; `toolCallTimeout` defaults to 60 seconds per call.
  Both are per server, are read when the configuration loads (changing one
  needs a restart), and are not clamped by any maximum.
- An unparseable or non-positive bound is a configuration error that names the
  key, so a bound the client would otherwise ignore cannot be mistaken for a
  protection that is in place.
- A server that misses its startup bound fails the command rather than being
  skipped, matching the rest of `mcpServers`: the file is the operator's own,
  and a run that silently lacks the tool set it asked for is worse than a loud
  failure.

## Deferred Systems

- Full platform memory extraction is not included. Retrieved memory can be
  adapted into normalized tasks through `adapters/memory`.
- MCP tools can be adapted through `adapters/mcp`, the CLI starts the stdio
  servers its `mcpServers` section declares, and `zenforge mcp-server
  --allow-run` lets another agent start a run here, but resources, prompts,
  sampling, elicitation, discovery, `listChanged` notifications, and OAuth
  flows remain host/platform responsibilities. MCP tool-call and handshake
  timeouts are constants rather than per-server configuration, and a
  session-wide MCP approval grant is not persisted from the CLI
  (`Config.ApprovalGrants` is an SDK surface).
- A served run answers the MCP call that started it: it is bounded by
  `--run-timeout`, but this server has no detached-run registry and no
  `zenforge_run_status` tool, so a caller whose own tool-call budget is shorter
  than the run reads the result back from the durable log instead of waiting.
  The run-starting grant itself is static (`--allow-run`): a stdio server has
  no operator at the keyboard, so there is no interactive or pending approval
  for it.
- OpenTelemetry exporter setup is not included; host services provide tracer
  providers/exporters and can use `trace/otel` as the sink adapter.
- YAML config is not included; the current CLI config format is JSON.
- Container Hub is optional and lives behind `sandbox/containerhub`.

## Agent Skills

- Filesystem catalogs index and snapshot bounded regular auxiliary files, and
  `load_skill` can disclose one indexed resource at a time. It does not resolve
  dependencies, install packages, update packages, verify signatures, or fetch
  remote resources.
- `skill/fs` accepts an explicit trusted root and rejects unsafe provenance and
  symlink traversal. The application still owns source trust and allowlists.
- Bundles are immutable startup snapshots. They do not provide live catalog
  refresh or marketplace lifecycle management.
- Skills do not grant tools, approval, workspace access, sandbox access, or
  sub-agent authority. Those controls remain in their existing runtimes.
- ZenMind marketplace entitlement, materialization, UI, and APIs are not
  implemented in core; a platform adapter may supply trusted catalog inputs.

## Platform Boundary

ZenForge should not import `agent-platform` or ZenMind server/chat packages.
Those systems can adapt to ZenForge through public model, tool, workspace,
approval, sandbox, event log, checkpoint, and trace interfaces.

`adapters/zenmind` has repository-local golden coverage for the
`agent-platform@1893edb5` catalog/session DTO subset, stream wire envelopes,
content/tool projection, approval roundtrip, and event-only chat JSONL lines.
`BuildRun` resolves host-owned skills, tools/overrides, and workspace/host
access and propagates executable runtime config, but does not load platform
catalogs or construct host services. Declared `HostAccess` and `ToolOverrides`
fail closed when their resolvers are absent.

Projector state is serializable for attach/resume. Strict projection requires a
v2 run binding; readable v1 snapshots remain unbound and cannot use
`ProjectStrict`. `ApprovalEventBridge` snapshots pending/completed correlations
and reconstructs awaiting wire values from real events, but the host owns
snapshot persistence, awaiting ID allocation, submit routing, and delivery.
Reused grant resolutions emit no answer because no awaiting request was opened.

This repository does not implement complete Chat Storage V3.1 or own platform
server wiring. That downstream wiring is implemented and tested on
`agent-platform` branch `codex/zenforge-engine-bridge@82ca4d3`, including the
engine selector, HTTP/SSE/WS, approval, attach, and legacy fallback. Platform
`main@f6d89da` restores this bridge, selector, routing, initialization, and
rollout documentation. The existing `agent-webclient` protocol tests and
production build pass, but deployed UI verification and a production Container
Hub deployment remain environment acceptance items; the adapter has an opt-in
disposable live-Hub test.
