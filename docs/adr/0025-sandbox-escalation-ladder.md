# ADR 0025: Sandbox Escalation Is A One-Shot Approval-Gated Ladder

Status: accepted

## Context

Sandbox denials are ambiguous. When a confined command fails with "operation
not permitted" or "read-only file system", the model cannot reliably tell
confinement from a genuine permission bug, and blind identical retries loop.
Both reference harnesses resolve this with an explicit ladder: codex
classifies likely sandbox denials and re-routes them into its approval flow
(`escalate-to-approval`), and DSH exposes model-visible `sandbox_permissions`
plus `justification` arguments for a strictly-wider one-shot retry that the
user adjudicates before anything spawns.

ZenForge's shell is deny-by-default (allowlist plus bash AST review, ADR
0010) and optionally confined by a sandbox `Backend` (ADR 0019), with no
silent fallback from sandbox to local execution (ADR 0020). What was missing
is a legitimate, user-approved escape hatch: today a confined denial is a
dead end even when the user would happily allow that one command to run
unconfined.

The mechanism also has to fit ZenForge's durable approval machinery (ADR
0004, ADR 0015): approval requests pause the run, brokers decide them,
decisions persist as grants, and retried tool calls carry approved metadata.

## Decision

### The ladder is model-visible only where it exists

The shell input gains `sandboxPermissions` and `justification` fields, and
the tool schema exposes them **only** when the shell is confined
(`Backend: sandbox`); an unconfined executor strips both properties so the
model is never offered an escalation that cannot happen. Pairing is strictly
validated before anything executes: both arguments or neither, the only
accepted mode is `danger-full-access` (escaping the sandbox backend for one
call), and the backend must actually be confined — anything else is an
invalid-arguments error.

### Escalation widens confinement, never policy

A blocked command stays blocked no matter what escalation arguments arrive:
the allowlist and bash security review run first and unchanged. Conversely,
leaving the sandbox **always** requires a fresh user decision, even for an
allowlisted command that would otherwise execute without any prompt.

The escalation rides the ordinary single-approval channel: the command review
is forced to require approval under a namespaced fingerprint
(`"escalate\x00" + fingerprint`), so approval metadata, durable grants, and
resume retries work unchanged, and the approval request is distinguishable
(`shell.escalate` operation, "Approve sandbox escalation" title, the
justification in the description). An approved escalation executes that one
call locally (`forceLocal`), stamps `escalated: true` on the structured
result, and never touches the sandbox session lifecycle.

### Denial classification is conservative and advisory

`policy.IsLikelySandboxDenied` mirrors the codex heuristic: exit 128+SIGSYS
(159) is always a denial; exits 2, 126, and 127 (shell misuse, not
executable, not found) are quick-rejected because escalation cannot fix
them; anything else non-zero is matched against failure-message dialects
emitted by sandbox executors and the kernels they delegate to ("operation not
permitted", "permission denied", "read-only file system", "failed to write
file", "seccomp", "sandbox", "landlock").

When a confined execution trips the classifier, the tool result gains
DSH-grammar markers — `[sandbox: file access denied under sandbox mode]`
plus a hint naming the exact one-shot retry shape — so the model learns the
escape hatch at the moment it is relevant. The classifier decides nothing by
itself: it annotates, and the user adjudicates.

OS-level sandboxes (macOS Seatbelt, Linux bwrap/seccomp) remain roadmap
items; the container backend plus this ladder is the current
confinement-and-escalation mechanism, consistent with ADR 0019 (sandbox is
an adapter) and ADR 0020 (no silent fallback — escalation is explicit and
approved, not a fallback).

## Consequences

Benefits:

- denial loops collapse into one legible, user-adjudicated decision with the
  justification attached;
- allowlist authority is untouched: escalation changes only where an
  already-permitted command executes;
- no new durable subsystem: grants, inboxes, resume metadata, and the
  approval event trail all work as-is through the namespaced fingerprint;
- the model-visible surface exists only when the executor is confined, so
  unconfined deployments never see dead arguments;
- result markers teach the exact retry shape in context, following the DSH
  marker grammar models are trained against.

Costs:

- classifier false positives annotate genuine permission failures with an
  escalation hint (advisory noise only; nothing executes without approval);
- an approved escalated call leaves container isolation for that one command,
  by explicit user choice;
- fingerprint namespacing means a persistent "always" grant for the plain
  command does not pre-approve its escalated form, and vice versa —
  deliberate, but one more scope for users to understand.

## Alternatives Rejected

### Automatic Fallback To Local Execution On Denial

Directly violates ADR 0020 and hides confinement failures from the user. The
whole point of the ladder is that leaving the sandbox is a human decision.

### A Second Approval Round-Trip Beside The Command Review

Approval metadata carries a single fingerprint, so requiring both a command
approval and a separate escalation approval in one call ping-pongs: each
retry overwrites the metadata the other gate checks. Folding escalation into
the review under a namespaced fingerprint keeps exactly one durable
decision per call.

### Model-Selectable Sandbox Modes

A ladder with one strictly-wider rung keeps the approver's decision binary
and auditable. Richer mode lattices (read-only, workspace-write) belong with
the OS-level sandbox work, where writable-root semantics actually exist.
