# Failure Modes

This guide documents how ZenForge behaves when a run is interrupted, a backend
fails, or an adapter cannot complete work.

## Run Cancelled

Context cancellation and deadline expiry are terminal cancellation outcomes,
not runtime failures. ZenForge records a `cancelled` checkpoint and emits
`run.cancelled`, including when the context is already cancelled before a model
or pending tool call starts.

Terminal event, trace, and checkpoint writes detach from the cancellation signal
while retaining context values so durable stores can record the outcome. A tool
call that was still pending remains in the cancelled checkpoint; a tool that
was already active follows the active-tool resume boundary below.

## Provider Stream Interrupted

ZenForge checkpoints at model-call boundaries, not mid-token. If a provider
stream fails while tokens are arriving, the run fails from the last checkpointed
boundary. Resume retries from that boundary instead of replaying partial
provider chunks.

Design implication:

- model providers should be idempotent enough for a retry from the last
  checkpointed prompt;
- UI/read-model adapters should treat streamed deltas as observable history,
  not authoritative resume state.

## Final Answer Requests Tools

Max-step finalization and plan/execute summaries set `ToolChoiceNone`. If a
provider still returns tool calls, ZenForge treats the response as a runtime
failure instead of executing the calls or emitting an empty successful result.

For regular and plan/execute runs, the failed checkpoint records the provider
contract error. Resuming that terminal run emits the same `run.error` without
calling the model or tools again.

Plan/execute orchestration failures outside the model/tool loop, including an
empty plan or invalid todo transition, also become durable terminal
checkpoints. Resume reports the stored failure instead of restarting planning.
If that terminal checkpoint cannot be saved, ZenForge reports the checkpoint
error instead of claiming that the original failure or cancellation is durable.
The previous checkpoint remains unchanged and is still the resume boundary.

## Active Tool Interrupted

If a process crashes while a tool call is active, resume moves the active tool
back to the pending queue and retries it with checkpointed arguments.

Tool authors should make side effects retry-aware:

- use idempotency keys or fingerprints for external writes;
- write temporary files before atomic moves;
- return durable operation IDs when a remote system accepts work;
- require approval for operations that are expensive or hard to reverse.

The generic retry middleware retries only errors explicitly wrapped with
`tool.MarkRetryable`. This prevents permanent failures, approval requests,
budget errors, and unknown side effects from being repeated automatically.

## Waiting Approval

When a run is waiting for approval, the checkpoint stores the approval request,
tool call, operation, risk, and payload. Resume emits `approval.requested` again
with `resumed: true`, waits on the configured broker, then either continues the
tool call or records the denial/expiry.

Without a broker, approval-required tools pause durably. ZenForge checkpoints
the waiting request and active tool, emits `approval.requested`, and stops the
current stream without `run.done` or `run.error`. `Agent.Run` returns
`approval.ErrRequired`; the run can later continue through `Resume` with a
broker.

Approval rejection is a tool error that the model may handle. Approval abort is
a run cancellation: the resolved decision is checkpointed for audit, followed
by a cancelled terminal checkpoint and `run.cancelled`. The synchronous
`Agent.Run` helper returns cancellation outcomes as errors compatible with
`context.Canceled` or `context.DeadlineExceeded`.

## Shell Commands

ZenForge does not assume a shell command completed if the process crashed while
the command was running. On resume, the tool call can be retried from the saved
arguments.

Recommended shell policy:

- keep shell deny-by-default;
- approve risky commands explicitly;
- prefer read-only commands for autonomous loops;
- keep output caps and timeouts small enough for local recovery.

## Sandbox Unavailable

Sandbox adapters must not silently fall back to host execution when sandboxing
is required. If a required sandbox backend cannot open a session or execute a
command, ZenForge returns an explicit error.

This protects users from believing a command was isolated when it ran on the
host.

## Event Log And Checkpoint Divergence

Events are the read model. Checkpoints are the resume source of truth. If event
sequence lookup or append fails, ZenForge stops before further model, tool,
approval, or sub-agent progress. The caller receives an in-memory `run.error`;
the event that failed to persist is not published as successful observable
history.

Checkpoint writes also fail closed: ZenForge stops before the next model/tool
boundary or successful terminal event, emits `run.error`, and does not emit
`checkpoint.created` for the failed write. The last successfully stored
checkpoint remains the resume source of truth.

Trace sinks are a best-effort observability projection. Exporter failures do
not change execution or event-log durability; platforms should monitor and
retry trace delivery in their sink adapter when that guarantee is required.

For local durability, prefer SQLite or JSONL stores on a filesystem with normal
fsync semantics. For platform deployments, keep event and checkpoint writes in
storage systems with clear ordering guarantees.

## Sub-Agent Failures

Sub-agent failures are visible as subtask error events and tool errors. Parent
runs can continue only if the model or tool result handling has enough context
to recover. Nested sub-agents are disabled by default.

Child checkpoint lookup failures stop before child model execution; only
`checkpoint.ErrNotFound` permits a fresh child run. Child cancellation is
reported as a failed subtask result rather than successful empty output.

## Memory And MCP

Memory retrieval and MCP server operations are adapter concerns. ZenForge does
not infer tenant boundaries, discovery trust, OAuth scope, or retention policy.
Host platforms must apply those policies before adapting memory or MCP tools
into the harness.

## Hardening Evidence

Current automated coverage includes:

- resume tests for terminal, active-tool, JSONL active-tool, and
  waiting-approval states;
- sub-agent resume tests for terminal and non-terminal child checkpoints;
- CLI smoke tests for local OpenAI-compatible streaming and JSONL resume;
- SQLite event/checkpoint store tests;
- `TestSQLiteDurableRunSoak` for repeated durable local runs;
- `BenchmarkAgentRunStaticModel` for a stable benchmark entrypoint;
- docs link verification through `docs.TestMarkdownLinksResolve`.
