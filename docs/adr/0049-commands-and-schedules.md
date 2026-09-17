# ADR 0049: Canned Commands And Unattended Schedules

Status: accepted

## Context

Two conveniences from the reference turn a harness into something a team can
keep: **slash commands** (a checked-in prompt template invoked as `/name
args`) and **schedules** (a task that runs again on its own). Both are small
features with sharp edges.

Commands are prompt injection by design: a file in the repository becomes
instructions to the agent. If a command can also run shell, then a file in
the repository becomes an execution primitive. Schedules run without a human
watching, so every failure mode is worse: an overlapping run piles up, a
crash ends the schedule, a typo makes it never fire.

## Decision

### A command is a markdown file, and its front matter is validated

`commands.Load` walks a directory, one command per `*.md` file, with
subdirectories as namespaces (`git/commit.md` is `/git:commit`). Front matter
is a deliberately tiny `key: value` block — `description`, `argument-hint`,
`model`, `agent`, `allowed-tools`, `run-bash`. An **unknown key is an
error**: a silently ignored `allowed-tools` is a security surprise, not a
convenience. An empty body, an unclosed front matter block, a non-boolean
`run-bash`, and two commands resolving to the same name are all errors. A
missing directory is an empty catalog, because most repositories have no
commands and that is not a failure.

### Expansion is ordered so arguments cannot escalate

Expansion runs in two phases with a deliberate order:

1. **Authoring features** on the definition: `@path` includes (workspace
   confined, size bounded, count bounded, refused when they escape, resolved
   only at a word boundary so an email address is left alone) and `!`cmd``
   inline shell (only when the definition sets `run-bash: true`).
2. **Argument substitution** in a single pass over the result: `$ARGUMENTS`
   and `$1`..`$9`, with `$$` escaping a dollar and substituted text never
   rescanned.

The order is the safety property. Arguments often come from a webhook, a
schedule, or another agent, so an argument must not be able to introduce an
`@include` that reads a file the author never chose, or a `!`shell`` that the
author never opted into. Authoring features belong to the definition;
arguments are data. A single-pass substitution also means an argument
containing `$1` cannot expand recursively.

### Inline shell still goes through the shell policy

`run-bash: true` does not grant a shell. The expression is executed through
the same shell tool the agent uses — the same allowlist, timeout, output cap,
and sandbox escalation. A command file cannot widen its own privileges, and
with `--no-shell` an inline expression fails rather than running
unconfined. Without the opt-in, `!`cmd`` is left **verbatim** in the prompt
instead of being silently dropped: the author can see nothing ran.

### An unknown command name is an error, but only when it looks like one

Input starting with `/` is resolved as a command when the catalog is not
empty. A name that looks like a command (`/reviw`) but is not in the catalog
is an error listing the catalog, because silently running the literal text
loses the command the user meant. Input that is not a plausible command name
(`/usr/local/bin/go`, `/`, prose containing `/review`) is passed through
unchanged, so paths and URLs keep working.

### Schedule parsing is explicit and honest about its subset

`schedule.Parse` accepts `every 30s`/`every 5m`/`every 5` (bare numbers are
minutes), `@hourly`/`@daily`/`@weekly`, `@monthly` (as a real calendar
schedule, not 30 days), and a five-field cron subset supporting `*`,
numbers, ranges, lists, and steps. Names (`JAN`, `MON`) are **not** accepted:
a schedule that silently misses because of a typo is worse than one that
refuses to parse. Intervals are floored at one second and ceilinged at 24
hours, so `every 0s` cannot become a hot loop. `Next` returns the zero time
for a schedule that can never match again (31 February) and the caller stops
instead of waiting forever.

### A schedule survives failure and never stacks

The loop recomputes the next firing from the *start* of each run, so a slow
run delays the next firing rather than causing it to be missed. A failed
firing is logged to stderr and the schedule continues: an unattended schedule
that dies on the first error is worse than useless. Cancellation ends the
loop cleanly.

## Consequences

Benefits:

- a team can version prompts as reviewable files, with namespaces for
  related commands, and get the same behaviour from the CLI;
- commands cannot escalate: no shell without an explicit opt-in, and no
  shell outside the configured policy;
- arguments are data, so an untrusted argument cannot read files or run
  commands through a template;
- schedule parsing fails closed on typos and impossible dates, and the loop
  is testable with its task injected — no clock, no model, no sleeping test;
- a failed scheduled run is reported and does not end the schedule.

Costs and limits:

- the cron subset has no names, no `@reboot`, no seconds field, and no
  timezone field (the location is configurable in code but not on the CLI);
- commands are discovered from one directory; there is no per-user command
  directory, no argument validation beyond size, and no interactive
  argument prompting;
- a command's `model`/`agent`/`allowed-tools` are parsed and surfaced in the
  listing but not yet applied to the run: today they are documentation the
  loader validates;
- scheduled runs are sequential and in-process: a schedule does not persist
  across restarts and does not survive the CLI exiting, so it is for keeping
  a terminal open, not for cron replacement;
- there is no webhook trigger, so an external system cannot start a run
  (recorded in the parity row as remaining work).

## Alternatives Rejected

### Run Inline Shell Unconditionally

The reference allows it; here it would turn a checked-in prompt into an
arbitrary command executor with whatever the agent's shell policy happens to
allow. Opt-in plus the shared policy keeps the feature honest.

### Substitute Arguments Before Includes

Simpler to write, and it lets an argument read any workspace file or run any
allowed command. Ordering authoring features first removes that class of
injection entirely.

### Accept Cron Names And Assume UTC

Names hide typos, and a silently wrong schedule is worse than a refused one.
The location is explicit in the type so a server can pin it.

### Persist Schedules In The Run Store

A durable scheduler needs ownership, leases, and catch-up semantics — the
same machinery the reference carries in its control plane. That is a bigger
commitment than this round's value, so the CLI loop is explicitly
in-process and the gap is recorded rather than papered over.
