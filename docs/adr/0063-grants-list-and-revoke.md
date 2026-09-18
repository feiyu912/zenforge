# ADR 0063: A Standing Grant An Operator Can See And Take Back

Status: accepted

## Context

ADR 0062 made "always allow this tool" outlive the run that made it, and
recorded the part it did not finish: there was no way to *look at* the grants a
namespace holds, or to take one back. The store could do both halves —
`Revoke` has always been there — but nothing enumerated them: `GrantStore`
exposes `Get`, `Put`, and `Revoke`, and the sqlite store had no listing query.
An operator could only reset the whole file by deleting it.

That is an incomplete operator story for the reason permissions are difficult
in the first place: authority that accumulates without a way to inspect it is
authority nobody can reason about. A standing allow is only a considered
decision if the operator can answer "what have I allowed?" and "how do I undo
that one?" later.

## Decision

### Listing is an optional half of a store

`approval.GrantLister` is a separate interface from `GrantStore`: a run only
resolves, writes, and (rarely) revokes, while enumerating a namespace is what
an operator's surface needs. Both shipped stores implement it —
`MemoryGrantStore.List` and `sqlite.Store.List` — and they agree on the shape:

- only the requested namespace's grants, with expired ones filtered out, so a
  listing never shows authority that is no longer in force;
- ordered by rule key and then by fingerprint, which puts a rule's standing
  grant before its payload-pinned entries;
- `Scope` normalized through `EffectiveScope` on read, so a reader never has
  to derive the scope from the fingerprint's absence (an older row without a
  scope comes back as the payload-pinned grant it is);
- a copy, not the store's own state.

A command that finds a store without the new half says so ("this grant store
cannot list grants") rather than printing an empty list, because "no grants"
and "cannot tell you" are different answers.

### `zenforge grants` is the surface

```bash
zenforge grants list [--json] [--grants-file FILE] [--tenant T] [--subject S]
zenforge grants revoke RULE_KEY [--fingerprint FP] [--grants-file FILE]
zenforge grants revoke --all
```

- `list` prints one line per grant — rule key, scope, action, granted-at,
  expiry — and a pinned entry is labelled with its fingerprint so a rule's two
  entries cannot be confused. `--json` prints the grants themselves, with the
  scope explicit.
- `revoke RULE_KEY` removes the *standing* grant for that rule: that is the
  broad one, so it is the one a bare rule key must mean. `--fingerprint`
  selects a payload-pinned entry instead, which is the only way to remove one
  without touching the standing grant.
- `revoke --all` walks the namespace's listing and takes back everything in
  it. An empty namespace is reported as such and is not an error, matching
  `zenforge runs` printing "no runs found".
- A rule key that has no grant is an error naming the rule, the scope word,
  and the namespace. A typo must not look like a successful revocation.
- The file comes from `approval.grantsFile`, resolved exactly as the agent
  resolves it, or from `--grants-file` for an operator who keeps several. The
  namespace comes from `--tenant`/`--subject`, then the configuration, then
  the same defaults the agent uses (`cli` and the operating-system user). No
  grants file is a usage error naming the key: the command does not invent a
  store to be helpful.
- Flags may appear before or after the subcommand and its rule key. The
  command collects them out of the line before parsing, because Go's flag
  package stops at the first positional and the subcommand is one — `grants
  revoke rule --config x` has to work as naturally as the other order.

## Consequences

- The gap ADR 0062 recorded is closed: a persisted allow can be listed,
  attributed to a namespace, and revoked individually or wholesale.
- `list` is read-only, so it is safe to run while an agent is running. `revoke`
  is not coordinated with a run that is already using a grant: the decision it
  already reused stays valid for that call, and the next call consults the
  store again. Revoking a grant therefore takes effect from the next approval
  check, which is the honest semantics of a store that is read at decision
  time.
- `--all` is deliberately not confirmed interactively: every other destructive
  action in this CLI is an explicit flag or an approval, and a revoke that
  prompted would be the only command in the binary that could not run
  unattended.
- The listing is the store's own view, so it shows what a *future* run would
  find. It does not show the grants a currently running agent holds in its run
  state, which are not persisted and die with that run.

## Alternatives Rejected

### Add `List` to `GrantStore`

Every implementation would then have to answer it, including an embedding
host's store that only ever resolves. The optional interface is the pattern
this repository already uses for capabilities a store may not have, and it
lets a command report the difference instead of pretending.

### `revoke RULE_KEY` removes every entry for the rule

The standing grant is the broad authority; a bare rule key has to mean the
thing that is dangerous to leave in place. Removing the pinned entries too
would silently widen one revocation into several, and `--all` already exists
for that intent.

### Revoke by request id or by index in the listing

A request id is not visible in the listing an operator reads, and an index
changes as soon as anything else is revoked. The rule key plus the optional
fingerprint is what identifies a grant in the store, so it is what the command
takes.

### A `grants` command that defaults to a conventional file path

Then `grants list` would silently create or open a database the agent never
wrote to, and print "no grants" for an agent that persists elsewhere. Naming
the file is the same opt-in as persistence itself.

## Verification

`go test ./approval/...` (both stores list only the namespace's live grants,
in order, with the standing entry before the pinned one, normalize the scope
on read, and hand back a copy; the sqlite test revokes the standing entry and
then walks the listing to empty the namespace), `go test ./cli/` (the command
lists a standing grant and a labelled pinned entry without leaking another
subject's grants, prints JSON with explicit scopes, revokes a standing grant,
a pinned grant without touching the standing one, and everything at once,
reports a missing grant with its namespace, refuses to run without a
configured file, takes the file as a flag, and rejects bad usage), `go test
./... -count=1`, `go vet ./...`, and `gofmt -l`.