# ADR 0062: A Standing Approval Is A Rule, Not An Argument String

Status: accepted

## Context

Three places in this repository define what a *rule-scoped* approval means,
and until now they did not agree.

- The tool side accepts one: `approval.MatchesApprovedMetadata` lets a call
  through when the call's metadata carries the approved rule key, whatever
  the arguments were.
- The in-run grants mean one: `reusedApprovalDecision` matches a rule grant on
  its rule key alone, so "always allow this tool" inside a run covers the
  tool.
- The durable store meant something narrower. `persistApprovalGrant` wrote both
  the rule key *and* the call's argument fingerprint, and
  `reusedPersistentApproval` required an exact match on both. A persisted
  "always allow this tool" therefore only replayed for byte-identical
  arguments — the one thing a model rarely repeats.

Nothing could even reach that path from a client. The MCP adapter's gated
requests offered `approval.DefaultOptions()`, which is approve-once and
reject-once; no broker could answer with a rule decision, so nothing was ever
persisted. And the CLI never configured `Config.ApprovalGrants` at all: the
grant store was SDK-only. The parity plan recorded the intent as "always allow
this tool is per-tool, while a run-scoped grant is per-payload" (ADR 0055);
the first half was true inside a run and false across runs, and unreachable
from the CLI either way.

## Decision

### The grant's shape says what it authorizes

`approval.Grant` gains an explicit `Scope`. A rule grant pins **no**
fingerprint: the rule key is its identity, and the absence of a fingerprint is
what makes it cover every call to the tool. `EffectiveScope` keeps the meaning
of an existing store — a grant that names no scope and pins no fingerprint is
a rule grant, one that pins a fingerprint is a payload-pinned grant — and
`Validate` refuses the two inconsistent shapes (a rule grant that pins a
fingerprint, a run grant that pins none) instead of guessing.

`GrantStore.Get(namespace, ruleKey, "")` is therefore the rule lookup, and the
memory store's key builder no longer requires a fingerprint. The sqlite schema
is unchanged: the empty fingerprint is simply another key value, so an
existing grants file keeps working and needs no migration.

### The agent persists the rule and reads the rule

`persistApprovalGrant` still persists only rule decisions — "this call" is not
a standing permission, so a once-scoped or run-scoped approval stays in the
run state where the run forgets it. What changed is what it writes: the rule
key with no fingerprint. `reusedPersistentApproval` looks up the rule entry
first and the fingerprint-pinned entry second, so a store written by an
earlier version still resolves, and the reused decision reports the grant's
`EffectiveScope()` rather than assuming one.

### A gated remote call offers the standing option

`mcp.tool` approval requests now append a rule-scoped approve option —
"Always allow this tool", described as covering every future call whatever its
arguments — to the once/reject pair. Only gated calls have a request at all,
so a read-only tool can never be given a standing grant by accident, and a
destructive tool can, deliberately: the operator is the one who decides
whether a standing grant is acceptable for a destructive tool, and the
reference offers the same choice. A rule decision means the same thing in all
three places now.

### Persistence is opt-in, and its namespace is not invented

The CLI's `approval` section gains four keys:

```json
{
  "approval": {
    "mode": "prompt",
    "grantsFile": "/home/me/.local/state/zenforge/approval-grants.db",
    "grantTtl": "720h",
    "tenant": "cli",
    "subject": "me"
  }
}
```

- `grantsFile` is the whole opt-in. Without it nothing is opened and a
  standing decision stays in the run that made it, which is where it has
  always lived; with it, the file is a sqlite grant store owned by the
  command (registered as a closer, so every exit path drains it).
- `grantTtl` bounds how long a persisted grant stays valid; empty means it
  does not expire.
- `tenant` and `subject` are the namespace the grants belong to, so a grant
  recorded for one identity is never replayed for another. They default to
  `cli` and the operating-system user name — the file already lives in that
  user's environment, and a shared default would defeat the isolation the
  namespace exists for. If the user cannot be determined, the operator has to
  say who this is rather than getting a grant attributed to nobody.
- A TTL, a tenant, or a subject without a grants file is a configuration
  error. A key the operator wrote and the client silently ignored is a
  protection that is not in place.

## Consequences

- "Always allow" now means across runs what it already meant inside one: the
  tool, any arguments. The gap that made a persisted grant nearly useless is
  closed, and the three implementations agree.
- A standing grant is authority that outlives the process, so the grants file
  is a credential-shaped artifact: it must be readable only by its operator,
  and its namespace is what keeps one operator's grant from answering for
  another's. A run-scoped or once-scoped decision still dies with its run.
- The store is opened before the agent is built, so a file that cannot be
  opened (a bad path, a directory, a permission problem) fails the command
  instead of silently running without persistence.
- Recorded gap, deliberately: there is still no way to *list* or *revoke* a
  persisted grant from the CLI. `GrantStore.Revoke` exists and the `grants`
  command is the next batch (ADR 0063); until then, deleting the file resets
  the store. A standing allow that an operator cannot inspect is the one part
  of this feature that is not yet an operator story, and it is recorded here
  rather than left implied.
- `Config.ApprovalGrants` stops being SDK-only: any embedding host can now be
  told to persist, and the CLI is simply a host that reads the file from its
  own configuration.

## Alternatives Rejected

### Keep the argument-pinned store and call it per-tool

Then "always allow this tool" would remain a label that does not describe what
was stored, and the same decision would mean one thing inside a run and
another across runs. A permission model that changes meaning at a process
boundary is worse than a narrower one that says what it does.

### A wildcard fingerprint value instead of an empty one

`"*"` would be a second way to spell "no fingerprint" and would have to be
special-cased in every store, key builder, and comparison. The empty string is
already the absence of the value, and making it a legitimate key value is one
validation change instead of a convention.

### Persist every scope

Persisting a run-scoped or once-scoped decision would make "this call" outlive
the run it was decided in. Only rule decisions are standing permissions; the
rest stay in run state.

### Persist by default at a fixed path

An operator who never asked for persistence would get authority accumulating
in a file they do not know about. Naming the file is the opt-in, and it is
also how a host that wants per-project grants keeps them apart.

### Require tenant and subject always

Three keys to turn the feature on is friction for no safety gain: the file is
already the operator's, and `cli` plus the operating-system user is an honest
attribution. Requiring them when the platform cannot say who the operator is
keeps the isolation intact where a default would be a guess.

## Verification

`go test ./approval/...` (a rule grant round-trips with no fingerprint and a
run grant without one is refused), `go test .` for the root agent
(`TestAgentPersistentRuleGrantCoversDifferentArguments` persists a rule
decision in one run, reuses it in a second run whose arguments differ, and
asserts the stored grant has no fingerprint and a rule scope; it fails when
the grant is pinned to the arguments again), `go test ./adapters/mcp/` (a
gated call offers a rule-scoped option that resolves against the request's own
rule key, and a read-only tool still skips the gate), `go test ./cli/` (the
section parses and validates the four keys, an unconfigured client gets no
store and no namespace, a configured one attributes `cli`/operator defaults,
registers the store as a closer, and a rule grant survives closing and
reopening the file — which is the next process), `go test ./... -count=1`,
`go vet ./...`, and `gofmt -l`.