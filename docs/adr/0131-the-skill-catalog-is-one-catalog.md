# 0131. The skill catalog is one catalog, seen by the panel and by the model

- Status: Accepted
- Date: 2026-09-22
- Supersedes: nothing
- Related: 0099 (the console adapter tier), 0128 (a refusal names the missing
  capability)

## Context

The console's skills panel calls `skills/list` with one argument, `sessionId`
(`@deepseek-ai/dsh-api-session-controller`, namespace `skills`). The reference
implementation resolves the session's own composition first -- its working
directory and agent preset -- reads that scope's skill registry, keeps the rows a
person may invoke, and maps each to `name`, `description`, the optional
`whenToUse` the skill declares, and `modelInvocable`. An unknown session is the
reference's own `session "<id>" not found`; a registry that fails mid-read is
`skill listing failed: …`.

The framework half of this host already had a skill catalog -- `skill.Catalog`,
`skill/fs` over a directory of `SKILL.md` files, and `skill.NewBundle` producing
the `load_skill` tool and the advertised catalog prompt -- and
`zenforge.Config.Skills` already carried a bundle into a run. Nothing wired it
up. `zenforge` had no skill flags, no catalog, and never set `Config.Skills`, so
a run advertised no skills and the panel had nothing truthful to list. The CLI
also knew only the Agent Skills frontmatter fields (`name`, `description`,
`license`, `compatibility`, `metadata`), while upstream files carry invocation
policy (`disable-model-invocation`, `user-invocable`) and routing guidance
(`whenToUse`) that the panel is required to report.

## Decision

**One catalog, read by both halves.** The host has a two-layer skill catalog: the
workspace's own `.zenforge/skills` (flag `--skills`) and a per-user directory
(flag `--user-skills`, config `skills.userDir`). A workspace skill of a given name
wins, the layer rule the slash-command catalog already follows. A directory that
does not exist is an empty layer, because most installations have no skills; a
directory that exists but cannot be scanned fails the read rather than reporting
an empty panel. The same catalog feeds `Config.Skills` (so a listed skill is
loadable by the run the session starts) and the panel.

That coupling is the point: a panel that listed a skill the agent could not load
would be a lie, and a run advertising skills the panel never shows would be
invisible state.

**Upstream's fields are read, not approximated.** `Descriptor` gained `WhenToUse`,
`DisableModelInvocation` and `DisableUserInvocation`. The invocation fields are
the negatives of upstream's frontmatter, so their zero value is upstream's
default (a skill is invocable by both) and a descriptor written by hand cannot
silently flip a policy. `skill/fs` reads `whenToUse`,
`disable-model-invocation` (booleans: `true`/`false`/`1`/`0`) and `user-invocable`,
and refuses upstream's retired camelCase spellings (`disableModelInvocation`,
`modelInvocable`, `userInvocable`) with upstream's own sentence -- a half-read
policy would advertise a skill its author asked to hide.

**A bundle is the model-facing view.** `NewBundle` drops a skill that disables
model invocation from the advertised prompt, from the `load_skill` tool, from the
allowlist and from the bundle's fingerprint. The catalog is untouched: the panel
still lists that skill, with `modelInvocable: false`, which is exactly the badge
that tells an operator the model will not find it on its own. The panel's own
filter is upstream's: a skill that disables *user* invocation is left out of the
rows.

**The method validates the session, then answers the host.** `skills/list`
rejects unknown arguments and a missing `sessionId`, answers the reference's
`session/not-found` sentence for an id this host does not know, and always
returns `{skills: [...]}` as an array -- an empty catalog is a fact, and a missing
key would read as a failure. A catalog that cannot be read answers
`skill listing failed: …`; a host started without the seam answers
`unimplemented`, naming `SkillSource` and `Handler.SetSkills`, the same shape the
slash-command catalog uses for its own missing dependency.

## Deviations, recorded

1. **One scope, not one per session.** The reference resolves the catalog from
   the session's composition (its cwd and preset). This host has a single
   workspace and a single host-wide catalog, so `sessionId` is validated but does
   not select a different set of rows; every session of a given host sees the
   same skills. Serving a per-session view would mean inventing scope the host
   does not have.
2. **`path` is not sent.** The row schema allows one; the reference does not
   populate it and neither does this host, rather than inventing a value.
3. **`whenToUse` travels on the rows, not in the prompt.** The host's catalog
   prompt advertises `name: description`, its own rule; `whenToUse` is reported to
   the panel. The live evidence below records the request the model actually
   received: `Available skills:` with the visible skill, without the model-hidden
   one, and without the guidance line.
4. **The skill set is part of a run's identity.** The framework records the
   bundle's fingerprint in a run's state and refuses to resume a run whose skills
   are gone. Wiring a catalog into serve therefore makes removing a skill
   directory a resume-time error for runs started with it. This is the existing
   framework rule, now reachable from the console; it is documented in
   `docs/limitations.md` rather than hidden.

## Consequences

- `zenforge` now has a skill catalog: `--skills`, `--user-skills`,
  `skills.userDir`, and a run that advertises `load_skill` when the catalog is not
  empty. A host with no skills sets no bundle at all, so no run claims an empty
  catalog and no run is fingerprinted for skills it does not have.
- `skills/list` is served (ADR-0131 row in the coverage ledger), taking the ledger
  to 49 served, 4 streams, 17 refused, 39 unserved.
- Live evidence, from a `zenforge serve` on this commit with a workspace holding
  two skills (`review`, with `whenToUse`, and `operator`, with
  `disable-model-invocation: true`):
  - `skills/list` answered both rows, `operator` with `"modelInvocable": false`
    and `review` with its `whenToUse`;
  - an unknown session answered
    `session/not-found: session "run-nobody" not found {"sessionId":"run-nobody"}`;
  - an extra argument answered
    `gateway/arguments-invalid: unexpected argument "cwd"`;
  - the model request of a run started from that host (recorded by pointing the
    provider at a local endpoint that logs the body and refuses it) contained
    `Available skills:\n- review: Review a change for correctness before it ships`,
    offered `load_skill`, and contained neither the model-hidden `operator` skill
    nor the `whenToUse` line.