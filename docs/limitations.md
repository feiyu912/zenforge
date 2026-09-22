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

## Browser Console

- The console `zenforge serve` offers at `/` is the **rebranded upstream DSH
  console** (ADR 0079), served from `webui/dsh` with its boot graph injected into
  the shell and its bundles under `/plugins` (ADR 0080). The first-party console
  of ADR 0078 is **deleted**, so `/classic/` is a 404 (ADR 0093).
- Its model configuration is **host-side** by upstream design: a browser session
  sends no credentials. Give the endpoint and key to the process (`--base-url`,
  `--model`, `--api-key`, or the environment) and the console's model picker
  reports that configuration through `session/modelCatalog`. The console's own
  settings panel can also write the endpoint, model and key (ADR 0087); the key
  is write-only and the write refuses what this host cannot hold. What the panel
  writes is durable: one `0600` document in the host's own configuration
  directory holds the endpoint, the model, the key, the declared profiles and the
  settings revisions, and it outranks the startup flags for every field it names
  (ADR 0102).
- **A custom provider can be declared, and the host says whether it can serve it**
  (ADR 0095). The Models page's "Add a custom provider" entry point is served: the
  `llm-pi-ai` namespace is reported with a schema whose protocols union names exactly
  the two wire protocols this host builds (`openai-completions`,
  `anthropic-messages`), a declared profile is validated by shape *and* by building
  the adapter a run would build, and a route that cannot be served yet is stored with
  its reason (for example a credential that is not set) and reported in the provider
  directory as declared. A declared profile is written to the host's settings
  document and read back at the next start, like every other console-written
  setting (ADR 0094, ADR 0102); it **is selectable and
  runnable** (ADR 0096); a draft endpoint can be interrogated for its model list,
  which is what the editor's fetch button does (ADR 0097); and it can be
  **corrected after it exists** -- the editor's per-field saves merge into the
  stored profile and a write that would leave the profile unserviceable is refused
  and changes nothing (ADR 0098). Two limits belong here: a field write is accepted
  one level deep only (`providers.<route>.<field>`, which is what the editor
  addresses), and a model field this host does not store (`id`, `name`,
  `contextWindow`, `maxTokens`) is refused rather than kept verbatim, so a model
  adopted from an endpoint that disclosed an input-modality list is named and
  refused instead of quietly trimmed.
- **Multi-turn works** (ADR 0086, ADR 0108, ADR 0114): a session's finished run continues
  as `<session>~<turn>`, so a second message is a new turn rather than a refusal,
  the conversation's turns share one sequence, and the follow stream stays open across
  them, so the second prompt loads its history and `Load earlier` reaches the first turn
  instead of being undone by a reconnect.
- **The live answer's token counts and end state are served** (ADR 0124): an attempt
  streams a `usage` chunk in the console's own token names and a `finish` chunk whose
  reason is `stop` or `tool-calls`, and both are also in the compacted stream, so a
  console that loaded the session reads the same usage pill and end state as one that
  watched it live. A run cancelled or failed while a tool was running now answers that
  call with `isError: true` and `error: {name: "Interrupted", code: "interrupted"}` --
  the console's own vocabulary, which it renders as "stopped" -- instead of leaving the
  card running forever. What is still missing: a failed tool keeps only `isError: true`
  (upstream has no execution-failure code to mirror, and inventing one would be a label
  the console cannot translate), and a tool call's arguments do not stream as
  `tool-call-delta` records, so they appear at once with `tool/call` rather than typing out.
- **The chat flow's prompt cards are served** (ADR 0123): a run writes the assembled
  system prompt as a durable `system.prompt` at the step that first sends it, and the
  console renders it from its own `system/message` surface event -- one node per
  assembled section, in the order the model received them -- with the request header
  (`provider`, `model`, and why it was logged) anchored beside it. The prompt is the one
  the model actually received, not a reconstruction from the flags the host holds. What
  is still missing: `request/context` (the context window and prompt-update record) is
  not emitted, because the shipped console has no consumer for it and this host records
  no per-request context window.
- **The sidebar survives a restart** (ADR 0109): `zenforge serve` keeps a SQLite
  run registry in its state directory (`<checkpoint-dir>/run-registry.sqlite`), so
  the console lists the conversations this install served after the host restarts,
  instead of losing them at the ten-minute terminal retention. A conversation whose
  host was killed mid-turn is listed as finished and can be continued, because a
  record whose lease expired is not reported as running. A session that never
  started a turn is still process-local (ADR 0104).
- **Assistant prose streams** (ADR 0116): the follow stream mints the console's
  dense `assistant-stream` frames from the harness's own durable deltas, so an
  answer renders token by token instead of appearing when its step settles, and a
  reconnect in the middle of one is handed the open attempt as its baseline, so the
  partial answer is on screen again before the next chunk arrives (ADR 0118). The
  honest limits: tool-call arguments do not stream (this harness emits a complete
  `tool.call`), and no `usage` chunk is sent (the accounting rides the settled
  message). A file resource renders because the host answers its `ready`
  subscription, but no `change` frames follow: this host watches no files, so a
  rewritten file shows its version at open time until the tab is reopened
  (ADR 0120). The turn and step boundaries are
  served (ADR 0121), but `turn/end` carries three of the console's six reasons, the
  request's model header and context window are not served, and a failed attempt's
  partial stream is dropped rather than projected as `assistant/attempt`.
- **The served log is the console's log** (ADR 0117): the host's own durable
  events -- checkpoints, the model lifecycle, every streamed delta -- are not
  served as records and consume no sequence number, so one ordinary turn occupies
  ten window slots instead of forty-six and the settled message carries the
  answer's byte-exact timeline as its compact `stream`. The served sequence
  therefore numbers records rather than durable events; a console page left open
  across this change may be told its stream resumed behind what it applied and
  reload.
- The sidebar does not mutate live, background-job panels are empty, and there
  are no agent-emit frames: this harness has a per-run event log, no global change
  feed and no job projections. Session lists, event follow, the workspace file
  sidebar, commands, the model/settings panels, approvals and streamed answers do
  work. See ADR 0083.
- Every namespace a shipped panel calls is served or refused by name
  (ADR 0084-0097). Methods this host does not implement -- `terminal`,
  `subagents`, `officeToPdf`, the pending queue's editor -- answer a bare 404 or a
  named capability error, which is the intended per-feature degradation rather
  than a console failure, and the host logs the endpoint that was asked for, which
  is how the remaining ones get prioritised. Attachments are refused by name
  instead of left to a 404, because the panel that would ask for one exists
  (ADR 0128); forking is served by copying the source's completed turns
  (ADR 0129); and the desktop half is answered rather than built (ADR 0126).
- **The model picker works, and a declared provider can be run on** (ADR 0096).
  `session/modelCatalog` lists the configured route and every declared provider's
  models, `session/selectModel` accepts a selection and refuses one this host
  cannot serve, and the selection is published as the session's `modelSelection`
  projection (control baseline, follow snapshot and live control frames), which is
  what the composer renders. The selection is applied to this host's single model
  adapter before each of that session's runs, first turn and continuations alike,
  so a session that chose nothing gets the operator's configured model rather than
  the previous session's choice. The honest limits: two sessions running
  **concurrently** under different selections share whichever adapter was applied
  last, because the harness's task carries no model field and this host owns one
  adapter; no model-selection event is written to the session log; and no model
  exposes a reasoning-effort choice.
  The console *reads* the log through a projection (ADR 0105), not verbatim: the
  host's events are mapped onto the console's session vocabulary, an event with no
  console meaning produces no record at all, and the records are numbered as the
  console's own sequence (ADR 0117). What the projection does not yet carry: a
  structured provider failure identity on a tool result (a failed tool states
  `isError` on its block with no console-side name/code envelope). A session that has been created but has not
  started a turn -- the draft the console opens before its first prompt -- is
  process-local too: it is served as an empty history (ADR 0104), and a restart
  forgets it because it has no transcript to keep. Since ADR 0103 a session's chosen model is
  written to the settings document and restored on the next start, with two
  bounds: only sessions whose model an operator actually chose are recorded, and
  the 64 most recent choices are kept -- a session that chose nothing still gets
  the operator's configured model, and the runtime last-used hint is not
  persisted. A
  route whose credential is missing is still listed and selectable -- the catalog's
  `failures` names what is missing, and the run that follows refuses the prompt
  with that reason.
- **Model discovery reads what an endpoint discloses and nothing more** (ADR
  0097). The fetch button in the model list editor interrogates a draft endpoint
  once (`GET {baseURL}/models`), for the two protocols this host speaks, with the
  draft credential in that one request's header and nowhere else. Honest limits:
  capacities appear only where the endpoint discloses them under the spellings
  this host reads (`context_window`/`context_length`, `max_output_tokens`/
  `max_tokens`), nothing is inferred from an unknown field, there is no
  pagination (the response is read to 1 MiB), the call is bounded by a 20 second
  timeout, and a provider route this host ships but is not configured for answers
  an empty catalog rather than a fabricated model list.
- `session/create` refuses `cwd`/`workspaceId`/`agentPreset`: per-run working
  directories and presets do not exist in this harness.
- The staged artifacts are a built dependency, not source: they are committed
  with their pinned upstream revision, patch and recipe
  (`scripts/build-console.sh`), because the upstream build cannot be renamed at
  runtime (ADR 0079).
- The **console's settings document is not watched**: it is read at startup and
  rewritten whole on each committed change, so an edit made to the file by hand
  while the host runs is overwritten by the next write, and two `serve` processes
  sharing one configuration directory replace each other's document rather than
  merging them (ADR 0102).
- The **welcome notice's acknowledgement is durable**: the console writes it into
  the same `0600` document as the endpoint, the model and the credential, so it
  survives a restart and the notice stays dismissed (ADR 0094, ADR 0102).
- The console's browser-visible identity reads `zenforge` in its title, manifest,
  favicon name and brand copy (ADR 0092); prose and documentation keep the
  sentence-case `ZenForge` stylisation.

## MCP Sampling

- A sampled run reasons in text. `sampling/createMessage` has no tool field, so
  the client's model cannot call the harness's tools; the definitions are
  dropped, a request that requires a tool call is refused loudly, and the
  client's answer arrives as one delta plus done because the protocol is not
  streaming. Use sampling for text-only work and the server's own model when a
  run needs tools.
- `--sampling` is the operator's choice, and only on `mcp-server` beside
  `--allow-run`: a client that cannot sample fails the run with an actionable
  error instead of silently using a different model, because a run's answer has
  to be traceable to the model that produced it.

## MCP Resource Subscriptions

- Subscriptions are per connection and opt-in (`ServerConfig.ResourceSubscriptions`);
  the CLI server does not advertise them, because its resources are read from
  the checkpoint store on each request and it has no update source to notify
  from. A client of that server sees `subscribe: false`.
- `resources/templates/list` returns the whole set without pagination: the
  registrations are fixed for the life of the process, so a `cursor` is
  accepted and ignored rather than answered with a `nextCursor` that never
  advances.
- An update racing shutdown either completes its write or returns
  `ErrNotServing`; the deliverable-or-error split follows the write lock's
  order and is not forced further.

## MCP Approvals

- An approval gate is a question only when the client advertised elicitation;
  otherwise the run refuses the tool call with the same text as before. A
  refusal the client answered says the client declined, which is a different
  sentence from the one that says this server cannot ask — a run has to be able
  to tell those apart.
- A non-boolean answer is a denial, never consent, and an approval is granted
  for one call: a yes does not create a standing grant (use the `grants`
  command for that).
- A detached run can also elicit, bounded by the elicitation timeout rather than
  by the request it outlived.

## MCP Server Requests

- A server request needs a caller deadline and a client that answers. An
  unknown or late response is dropped, and a `Serve` return wakes every waiter
  with `ErrStreamClosed`; a request cannot outlive its stream.
- Elicitation is refused unless the client advertised the capability, so a
  caller must be able to fall back. The CLI's served runs still refuse an
  approval-gated tool rather than eliciting: wiring approvals to elicitation is
  a separate decision, and until then the refusal is the honest answer.
- Responses to two concurrently handled calls may arrive in either order; MCP
  pairs a response with its request by id, and a consumer that assumed ordering
  must not.
- On shutdown, in-flight handlers are waited for (so a client that closes its
  input but keeps reading still receives the responses it asked for) before the
  stream is detached. A client that stops reading altogether makes a write
  block, and that backpressure delays shutdown, exactly as a blocked stdout did
  before.

## MCP Notifications

- Progress notifications reach a client only when its request carried a
  `_meta.progressToken`; the token is echoed exactly as sent, and a handler that
  reports a non-finite value produces no frame rather than an invented number.
- `listChanged` is opt-in: the CLI server advertises `false` because its tool,
  resource, and prompt sets are fixed by the flags it was started with, and a
  `Notify*` call on a server without the capability errors instead of sending a
  notification the client was told not to expect. A `Notify*` call also needs a
  served stream; a caller driving the server's `Handle` directly cannot use it.
- Elicitation, sampling, `resources/subscribe`, `resources/templates/list`, and
  server-initiated requests are not implemented.

## Commands

- Commands come from two layers: the workspace's `.zenforge/commands` and the
  user's config directory (`commands.userDir`, default `~/.config/zenforge/commands`
  or the equivalent under `XDG_CONFIG_HOME`/`ZENFORGE_CONFIG_DIR`). A workspace
  command wins over a user command of the same name; the shadowed one is kept and
  listed as `(workspace overrides user)` rather than removed, so precedence stays
  visible.
- `commands.userDir` is deliberately not written into the default config file
  (`zenforge init`): the default is a resolved path, and freezing this machine's
  home directory into a project's config would be worse than omitting it.

## MCP Resources And Prompts

- `resources/subscribe`, `resources/templates/list`, `listChanged` and progress
  notifications, elicitation, and sampling are not implemented. The
  `initialize` capabilities say so (`subscribe: false`, `listChanged: false`),
  and a URI template such as `zenforge://runs/{runId}` is listed verbatim in
  `resources/list` rather than in a templates listing.
- Prompt arguments come from a command's `argument-hint`: `<x>` is required and
  `[x]` optional, a command with no hint but `$ARGUMENTS`/`$1`..`$9` gets one
  optional `arguments` argument, and a value containing whitespace is quoted so
  a positional stays whole — which means `$ARGUMENTS` sees those quotes.
- A prompt never runs inline shell (`!`...`` stays verbatim) and never reads
  outside the workspace (`@file` includes are workspace-confined).

## Webhook Runs

- The webhook endpoint (`POST /webhook/run`) is registered only when a secret
  is configured, and the signature covers `timestamp.body`, so a captured
  request cannot be replayed with a fresh timestamp; a timestamp outside
  -5 minutes/+1 minute is refused. There is no per-webhook scoping yet: one
  secret authorises every webhook run this server accepts, and it starts runs
  with the server's own workspace, tools, and approval mode.
- The reply is an acknowledgement (`202` with the run id), not the answer: the
  caller polls the run endpoints, and the durable store is where the result
  lives.

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
- MCP is bidirectional, and the two directions have different scopes. As a
  client, ZenForge starts the stdio servers its `mcpServers` section declares,
  exposes their tools as `mcp__<server>__<tool>` behind per-tool approval
  rules, and bounds each server with its own startup and call timeouts; it
  does not yet read a connected server's resources or prompts, nor answer an
  incoming elicitation or sampling request. As a server, `zenforge mcp-server`
  exposes runs as tools, resources, and prompts, serves resource
  subscriptions and progress notifications, can ask its client for
  elicitation and model sampling, and `--allow-run` adds a run-starting tool
  whose runs can outlive the call that asked for them (`--run-timeout` still
  bounds them; `zenforge_run_status` and `zenforge_run_cancel` reach them).
  OAuth and any transport authorization, the roots and logging conventions,
  and argument completion remain host/platform responsibilities, `listChanged`
  notifications require an explicitly dynamic server, and a session-wide MCP
  approval grant is not persisted from the CLI (`Config.ApprovalGrants` is an
  SDK surface).
- Inside a served run, approval-gated tools ask the calling client for a
  decision when it advertises elicitation and fall back to a refusal reported
  with the run's outcome when it does not; `--approve always` skips the
  question. The run-starting grant itself is static (`--allow-run`): a stdio
  server has no operator at the keyboard, so there is no interactive or
  pending approval for it.
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

## A console session runs in the host's one directory

The console groups sessions by workspace, and this host serves that grouping:
directories can be registered, renamed and removed, and the archived set is
host-side state (ADR 0101). What the host cannot do is *run* a session in a
registered directory other than its own. The harness agent carries exactly one
workspace and the run request has no workspace field to override it, so every
run of this process edits the directory `zenforge serve` was started with.
Selecting any other directory is refused by name -- the message names both paths
and the `--workspace` flag that would make the other directory the host's -- and
the session the console opens is always grouped under the directory it actually
runs in. Per-session workspaces need a framework change (a workspace override on
the run request) before an adapter can honor them.

## A registered workspace does not survive a restart

Workspace registrations, their titles and the archived session set are
process-local console state. The console's *settings* are not: since ADR 0102 the
endpoint, the model, the credential, the declared provider profiles and the
namespaces the console owns are held in a `0600` document in the host's own
configuration directory -- and since ADR 0103 the model each session chose is
held there too, bounded to the 64 most recent choices -- and the registry was
deliberately left out of that change -- a registered directory this host cannot run a session in (ADR 0101) is a
different question from a preference worth restoring. So a restart starts the
workspace list from the host's own directory again. A session's transcript and its
workspace files are unaffected: those live in the run logs and on disk.

## The goal dock is served, but its own creation path is not

The seven `goals/*` methods are served, and so are the `goal` projection and the
`goal/activation-changed` forward (ADR 0125): a session's goal is the framework's
durable goal state in `<checkpoint-dir>/goals`, so the composer's goal bar
renders the phase and objective, pauses, resumes, edits and clears it. Two
honest gaps remain in that panel:

- **The composer's `/goal` command is not implemented.** It is a built-in *host*
  command (`@deepseek-ai/dsh-command-goal`) with a client face the console
  renders, and `commands/execute` here resolves the workspace's file commands
  only -- so typing `/goal …` is refused as an unknown command, and the dock's
  own creation path is absent. A goal is created host-side instead: `zenforge
  serve --goals` registers the `create_goal` tool (the model can set one), and
  the durable document can be written by the command line and the goal tools.
  The dock then renders and mutates what exists.
- **`goals/clear` leaves no tombstone.** The reference retains the cleared goal's
  identity and the set of identities a session has used, so reusing an id or
  mutating with a pre-clear ref is refused as duplicate or stale. This host
  removes the state document, so both are `GOAL_NOT_FOUND`. The dock cannot
  observe the difference, and ADR 0125 records the deviation.
- **Activation is derived, not scheduled.** `armed` means an active phase with
  round budget left, computed from the durable state. This host's served runs do
  not run the framework's goal-continuation loop, so the bar reports eligibility
  rather than a live scheduler, and a goal does not continue by itself under
  `zenforge serve`; `zenforge goal` is the surface that iterates one.

## The host has no desktop to open a path on

The console declares two methods about the **machine serving it**: one asks
whether this deployment can hand a workspace path to the operating system's file
manager, and the other does it (`action: "reveal"` for a file-manager reveal,
omission for the default application). This host serves the console in a browser
and implements no desktop carrier -- the protocol recon listed the desktop bridge
as an explicit non-goal -- so the question is answered `false` and the operation
is refused by name, with the substitute named in the refusal (ADR 0126). In
practice there is no "reveal in Finder" or "open in the default application"
affordance behind the console, and a workspace file is looked at in the console's
own file browser (`workspaceFiles/list`, `workspaceFiles/read`) instead. Nothing
in the shipped page calls either method, so no visible control is lost; a
deployment that ships a desktop carrier is what would flip the answer and make
the open real.

## The sidebar's search has no index and no ranking

Searching the sidebar reads the conversations the list shows, on demand, out of
the same projected logs the transcript is served from (ADR 0127). That is enough
to answer the question and not enough to be an index: there is no ranked
relevance order (a conversation's row quotes its **newest** matching message, and
rows keep the list's newest-first order), every query re-reads the listed
conversations rather than consulting a prepared index, and the cancellation signal
the client sends has nothing to interrupt because this host's unary transport does
not carry one. On a directory with a very large number of long conversations a
query therefore costs more than it would against the reference's
`@deepseek-ai/dsh-session-query` provider, which this host does not mount. What
the answer does mirror is the contract the sidebar renders: one row per
conversation, the reference's twenty-result cap with `hasMore`, its 240-code-point
excerpt bound, and its own refusals for a blank, over-long or NUL-bearing query.

## Attachments cannot be sent or read

The console ships its attachment panel, so the composer offers an attach
affordance, but nothing behind it can work here: the upload route
(`fileUploads/upload`) is not served, a prompt whose content carries an image or
file part is refused by name -- this host accepts text parts only, and the run
manager has no attachment intake -- and the read half, `session/attachment`,
refuses by name for the same reason rather than answering a not-found for an id
that could never exist (ADR 0128). The working substitute is the workspace: a file
put in the host's directory can be read by the agent's own file tools, and its
content can then be discussed in the conversation. Serving attachments properly is
a subsystem, not a route: it needs an upload path, a store with the metadata the
console renders (`mediaType`, `bytes`, `width`, `height`), and the prompt path that
carries a reference into the run.

## A forked conversation is a snapshot

Forking works, and what it produces is a copy rather than a view (ADR 0129). The
child's turns are materialized from the source's completed turns at the moment of
the fork: continuing the source afterwards does not appear in the child, and
deleting the source leaves the child whole. The cut lands on a completed turn --
forking "at" a message keeps that message's turn and everything before it, never a
half-finished one -- so a `session/fork-unavailable` for a message inside a
running turn is the honest answer rather than a child with a dangling turn. The
copied records keep the times they happened at, while the child's own newest
record is stamped with the fork's moment so it sorts as the most recent
conversation. A forked conversation inherits its source's title, which is why the
console renames it to an increased one, and it runs on the host's *default* model
rather than the source's selected one, exactly as the reference composes it.

## A queued message lives with the run it was queued for

The console's queue dock works: the messages waiting for the running turn are
projected as the `inbox` cell, and each row can be edited, dropped or steered
(ADR 0130). What the queue is not is durable. Upstream folds pending input out of
the session's own events, so its inbox survives a restart and carries a message
into whatever turn comes next; here the pending messages are the live run
controller's, so they exist only while the run does. Two consequences are worth
knowing before relying on them: a message queued for a run that ends before the
model-turn boundary -- the turn was cancelled, or it failed -- is dropped with the
run rather than delivered to a later turn the operator did not aim it at, and a
host that restarts has no queue to restore. In both cases the console is told the
truth (the row leaves the cell), and the durable fold is a piece of work this host
has not built.

## The skill catalog the console shows is the one a run advertises

`zenforge` reads skills from the workspace's `.zenforge/skills` and from a
per-user directory, and the skills panel lists exactly what a run started from
that host can load (ADR 0131). Four things follow from that, and they are worth
knowing before relying on the panel:

- The catalog is host-wide, not per session. The reference resolves a session's
  own composition before listing; this host has one workspace and one catalog, so
  the session in the request is validated and then every session sees the same
  rows.
- `whenToUse` is shown to a person, not to the model. The probe the model receives
  advertises each skill as `name: description`; the routing guidance travels on
  the panel's rows.
- A skill that disables model invocation stays listed, with
  `modelInvocable: false`, and is absent from the probe and from `load_skill`.
  A skill that disables user invocation is left out of the panel entirely.
- The skill set is part of a run's identity. The framework records the bundle's
  fingerprint in the run's state, so removing a directory (or a skill in it) makes
  a run that started with that catalog refuse to resume, naming the missing
  bundle. Add a skill and the new run picks it up; the old run's checkpoint is
  pinned to the set it ran with.
