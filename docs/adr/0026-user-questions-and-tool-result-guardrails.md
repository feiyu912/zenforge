# ADR 0026: User Questions And Tool-Result Guardrails Ride Existing Runtime Channels

Status: accepted

## Context

Three DSH tool-runtime protections keep long sessions honest. First,
`ask_user_question`: when the model lacks a decision only the human can make,
it asks structured questions with stable ids instead of guessing, and the run
waits for the answer. Second, the spill store: only ~50 KiB of a tool result
stays inline, the full output moves to a private file the model can read back
on demand, and a head/tail preview marks the cut. Third, the
repeat-tool-reminder: consecutive identical tool calls are counted, and at
thresholds the model receives progressively firmer reminders so loops surface
instead of grinding.

ZenForge already owns the machinery each of these needs. The approval channel
(ADR 0004, ADR 0015) pauses runs durably, routes decisions through brokers
and inboxes, persists grants, and resumes with approved metadata — a
question round is structurally an approval request whose "decision" carries
answers. The tool middleware chain (ADR 0003) composes result post-processing
without touching individual tools. The workspace read tool can re-read any
file inside its read roots, so a spilled output file needs no new retrieval
path. The open question was whether to build parallel subsystems or reuse
these channels.

## Decision

Reuse the existing channels; add no new durable subsystem.

### askuser: questions as approval requests

`tools/askuser` implements the DSH contract: up to four questions per call
(`DefaultMaxQuestions`), each with a required stable id echoed in the answer,
optional structured choices (`label` plus one-sentence `description`), and a
`multi_select` flag; ids, question text, and option labels are validated,
and duplicates are rejected.

Durability rides the approval channel. An unanswered call returns a
`user.question` approval request (risk `low`, questions and a canonical
fingerprint in the payload) and pauses the run; any broker decides it.
Answers travel back in `approval.Decision.Payload["answers"]` keyed by
question id, and the new `approval.MetadataDecisionPayload` metadata key
propagates decision payloads into the retried tool call, which renders them
as its result — so resume replays the exact exchange, and the question-round
fingerprint lets an identical re-ask match the persisted approval instead of
prompting twice. An approval without answers (for example from
`approval.AlwaysAllow`) yields an honest "dismissed without answers" result.

The DSH root-agent-only rule holds: a call carrying subagent metadata
(`subtaskId`) fails with a recoverable error instructing the child to return
the question in its final report so the parent can ask the user. The
interactive CLI broker renders question rounds directly — an option number
resolves to its label, anything else is taken as free text, multi-select
accepts comma-separated numbers — and the exported `QuestionsFromPayload`
helper lets other brokers render rounds (including after JSON round-trips
through server inboxes) without depending on tool internals.

### tool.Spill: oversized results move to a private store

The `Spill` middleware moves result output larger than `MaxInlineBytes`
(default 50 KiB) to an on-disk store — directory mode 0700, file mode 0600 —
and keeps a UTF-8-safe head/tail preview plus a pointer naming the exact path
and full size inline. Filenames are idempotent per run, tool call, and
argument hash, so a retried call overwrites its own spill file. The CLI
points the store at `.zenforge/spill` under the workspace so the workspace
read tool can reach the full output; the library default is a private
per-process temp directory. Spill-store failures are fail-soft: the output is
then truncated inline with an explicit marker, so an oversized result never
reaches model context unchecked either way.

### tool.RepeatGuard: loops surface, never block

The `RepeatGuard` middleware counts consecutive identical calls per run (same
tool name and arguments; any different call in the run resets the streak) and
appends escalating reminder text to the real result at the DSH thresholds
`[3, 5, 8]` (`RepeatGuardThresholds`), with the streak also stamped into
result metadata. It never blocks or rewrites the underlying result: polling a
background job is legitimate, and the guard's job is to make the loop visible
to the model, not to police it.

The CLI composes the runtime as `[RecoverPanic, RepeatGuard, Spill]`
(outermost first), so results are spilled before reminders append — the
reminder text itself can never be spilled away.

Two deviations from DSH are deliberate. Reminders ride the tool result
instead of an injected plugin user message, which keeps them inside the
durable run history without a new message-injection channel. And the streak
counter does not reset on human input, because middleware cannot observe
steering messages; a steered conversation virtually always changes the call
signature anyway, which resets the streak naturally.

## Consequences

Benefits:

- question rounds are durable end to end: pause, resume, and replay need no
  new state machines beyond approval metadata;
- answers are id-keyed and typed (string or string slice), matching what the
  model asked, and the same broker ecosystem — CLI, inbox, GUI — serves
  questions and approvals;
- subagents cannot silently demand human attention; questions surface
  through the parent's report;
- context is protected from both directions: result size (spill) and
  repetition (guard), with zero new durable stores;
- guardrails are ordinary middleware: hosts reorder, reconfigure, or omit
  them without touching tools.

Costs:

- `ask_user` needs a broker that can collect answers; approve-only brokers
  produce "dismissed without answers" results rather than real exchanges;
- spill files accumulate on disk and cleanup is host-managed (private modes
  and the in-workspace default limit exposure);
- reminders and spill markers add bytes to results;
- the repeat-guard keeps one small streak entry per run for the lifetime of
  the middleware value.

## Alternatives Rejected

### A Dedicated Question Subsystem With Its Own Pause State

A second pause/resume mechanism beside approvals would duplicate durable
inboxes, grants, expiry, and event plumbing, and hosts would integrate twice.
The approval channel already models "run waits for a human decision".

### Inject Repeat Reminders As User Messages

DSH injects reminders as plugin user messages; ZenForge middleware has no
message-injection channel, and injected messages would need their own
durability and compaction semantics. Appending to the tool result keeps the
reminder exactly where the model sees the repeated call's output.

### Hard-Block Repeated Calls

Blocking at a threshold would break legitimate polling patterns and give the
model a confusing hard failure instead of a legible nudge. The reference
harnesses also treat repetition as advisory.

### Stream Oversized Results Unchanged Into Context

Unbounded tool output crowds out the conversation, inflates compaction
pressure, and can exceed provider limits outright. Spilling keeps the full
data reachable while bounding the inline cost, matching the DSH spill
contract.
