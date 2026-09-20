# ADR 0104: A Session Exists Before Its First Turn, And A Registered Session Has Not Chosen

Status: accepted

Amends [ADR 0102](0102-the-consoles-settings-and-credential-persist-in-a-host-owned-file.md)
(the registered-session records it introduced are read here as "no choice yet" rather
than as a choice). It does not add a remote method: `session/create`, `session/page` and
`session/follow` were already the console's own calls.

## Context

The host that ADR 0103 had just restarted could not hold a conversation. The operator
selected a model -- that part worked -- sent `hello`, and the page answered:

```
Failed to load history: session "run_1789874914388405000" not found (session/not-found)
```

with no reply. Two independent defects met on the first prompt of a new chat, and both
had to be fixed before the console could answer anything.

### The history of a session that has no turns yet was refused

The console does not create a session and prompt it in one step. It creates the session
first (`session/create`, which allocates an id such as `run_1789874914388405000`), opens
its history to render the empty conversation, and only then sends the first prompt. The
id at that moment names no run and no durable log, and both read paths treated that as a
missing session:

- `session/page` refused when the newest turn's log was empty;
- `session/follow` refused when the run was unknown *and* the log was empty, which is the
  same condition one transport over.

The author of the follow stream had already established, in its own comment, that the
client accepts an empty snapshot: the cursor for a log with no records is upstream's `-1`
and the snapshot frame had a branch for exactly that case. Refusing was not a protocol
requirement; it was the empty-log case being folded into the unknown-id case. A session
this host created is a session, and before its first turn its history is empty, not
missing.

### Every registered session's first prompt failed on a selection nobody made

`session/create` registers the session with the model-selection store so the console's
projection has a `modelSelection` key for it from the moment it exists (ADR 0102). That
record carries no choice. `ApplyModelSelection` tested the record's *presence* in the map
as though it were a choice:

```go
record, chosen := s.records[sessionID]
if !chosen {
    s.settings.rebuild()   // the configured model
    return nil
}
```

so a session that had chosen nothing was handed to `AdapterFor` with provider `""` and
model `""`, which failed with

```
session "run_..." has a model selection this host cannot apply:
provider "" is not one this host can route to (it serves openai)
```

and `session/prompt` refused before starting the run -- the "no answer". The registration
and the reader were written in different chains (the store's choice logic in `4f96c4d`,
the registration in `d6931c7`) and each was correct alone: a record without a choice is
only reachable because registration put it there. The flag the reader wants is the record's
own `selected`, which is what every other reader in that file already uses.

## Decision

### A created session's empty history is served, not refused

`session/page` answers a session the host created with no turns as an empty page
(`records: []`, `hasMore: false`). `session/follow` sends the empty snapshot (cursor `-1`)
and then waits for the run the first prompt starts.

The wait is a poll (`draftRunPollInterval`, 50 ms, bounded by the stream's own context):
the run manager exposes no "a run was created" notification, and a run that does not exist
has no per-run bus to subscribe to. The interval is short enough that the first turn's
first frame follows the prompt immediately, and the alternative -- ending the stream after
the snapshot and relying on the client to reconnect when the run starts -- would leave the
answer invisible on a page that had already opened its stream.

### Only the RPC handler knows which sessions are drafts

The sessions a host has created live in the RPC handler's pending set, and the follow
stream is a different package with no access to it. The mount exposes it as
`dshmount.Mux.IsDraftSession`, which asks the RPC handler
(`Handler.IsDraftSession`), and the stream takes it as a `Config.DraftSessions` func.

An id this host never created is still `session/not-found` on both paths. That distinction
is the point: a transport with no draft seam, or a host that never created the id, must not
invent an empty session for it.

### A registered session is not a session that has chosen

`ApplyModelSelection` falls back to the configured adapter unless the record says
`selected`. A registered record keeps its key in the projection -- the composer still shows
its model control -- while the run uses what the operator configured. The map's presence
answers "does this session have a projection?", never "did the operator choose?".

## Consequences

- A new chat works: the conversation opens empty, the first prompt starts a run, and the
  turn streams over the connection the page already opened. Verified against a host built
  from this checkout with the operator's own document copied to a throwaway configuration
  directory: `session/create`, an empty `session/page`, `session/prompt` accepted, and a
  real model answer recorded in the log.
- A session that chose nothing now runs on the configured model. That was already this
  ADR's sibling chain's intent (ADR 0103's `docs/limitations.md` entry says so) and was
  simply broken for every fresh session.
- Draft sessions do not survive a restart: the pending set is process-local, like the
  workspace registry (ADR 0101). A restart forgets a draft that never ran, and the console
  creates a new one on its next page load. Nothing durable is lost -- a draft has no
  transcript, no selection and no title.
- The wait is bounded by the client's connection, so a page that opens a draft and never
  prompts leaves one goroutine parked until the socket closes, polling one map lookup
  every 50 ms.
- `session/page` and `session/follow` still refuse an id this host never created. The
  unknown-session tests for both paths stay, now asserting the narrowed rule.

## Alternatives Rejected

### Treat any unknown id as an empty session

It removes the refusal without adding anything: a page that mistyped an id, or a client
that reconnects to a session from another host, would be shown an empty conversation
instead of an error, and the console's own "session not found" handling would never run.
The draft set is small, bounded, and exactly the sessions this host created.

### Start the run at `session/create`

The console's create and prompt are separate calls, and a create that started a run would
begin a turn with no input -- and would burn a model call for every draft the page opens.

### Persist drafts in the settings document

The document holds console-written settings (ADR 0102). A draft is not a setting: it has no
content, and the console recreates it on demand. Persisting it would add rows that mean
"nothing" and would survive a restart as a conversation with no turns.

### Wait for the run by polling the event store instead of the manager

The store answers "is there a log?", not "is there a run?". A run exists from the moment
`Start` returns, before its first event is durable, and the follow tail must attach to the
run rather than to a log that is momentarily empty.

## Verification

`go test ./internal/dshapi/ -run TestSessionPage` -- a session created with no turns answers
an empty page, `hasMore` false, and an id this host never created is still
`session/not-found`.

`go test ./internal/dshstream/ -run TestSessionFollow` -- a draft's follow stream sends the
empty snapshot (cursor `-1`, no records) and then streams the first turn's `run.started`
over the same connection, while an unknown id still ends the stream as
`session/not-found`.

`go test ./cli/ -run TestRegisteredSessionWithoutAChoice` -- a registered session with no
choice leaves the configured adapter in place, still has its projection key with no
selection, and a session that did choose still installs its own adapter.

`go test ./internal/dshmount/ -run TestMuxReportsDraftSessions` -- the mount reports a
session the RPC handler created as a draft, an id it never created as not a draft, and a
mount with no RPC handler reports none.

Live, against a scratch host carrying a copy of the operator's document (throwaway
`ZENFORGE_CONFIG_DIR`, its own port): `session/create` returned an id, `session/page` for
that id answered `{"hasMore":false,"records":[]}` instead of `session/not-found`,
`session/prompt` answered `accepted` instead of the empty-selection failure, and the run
finished with the model's answer in the durable log. The copy was deleted with the scratch
process.