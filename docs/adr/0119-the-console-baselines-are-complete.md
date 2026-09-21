# ADR 0119: The Console's Baselines Are Complete

Status: accepted

## Context

A file-by-file audit of our host against the client the console actually ships
turned up three wire deviations with one thing in common: the console is handed an
opening baseline and silently discards most of it. Two of the three were measured
in the browser against a real host.

1. **The control baseline omitted `queues`.** Upstream's is
   `{queues, jobs, projections}` and the key is required. The client's
   `replaceControlBaseline` runs `Object.entries(baseline.queues)` *before*
   anything else and throws on an absent key, so the throw discards the projection
   seeding that follows it and tears down the control stream's carrier — on every
   console load.
2. **The follow baseline cited `revision: 0` after replaying a turn.** ADR 0118
   made the follow stream rebuild its tracker from the durable log, which numbers
   the frames it replays. The snapshot still said `revision: 0`, while the client
   holds every later frame to `revision + 1` from the snapshot. Measured: after a
   reload mid-answer the baseline carried `nextIndex: 21` and the stream then
   delivered **zero** further frames — the client had rejected the first one as a
   skipped revision and torn the stream down.
3. **The conversation's name was served as a field of `session/list`.** The client
   never reads one: a list row seeds its projection store from
   `projections.values` and the header folds the same `title` cell, so the name it
   rendered was always the fallback — the raw session id. The durable title record
   was not served either, so nothing could fold it from the log.

## Decision

- **`queues` is sent, empty.** A host with no queue mirror still has to write the
  key: an empty map is the honest value, and omitting a required key is not a
  smaller answer, it is a thrown exception in the client.
- **The snapshot's revision is the generation's frame counter**, never a constant.
  After a replay it is the number of frames the replay numbered (including each
  attempt's `start` frame, which the client counts for revision but not for
  `nextIndex`); a generation that has sent nothing still says 0.
- **The name is a projection.** Both adapters publish `title` through the one key
  the vocabulary owns (`dshwire.TitleProjection`): the session list row carries
  `projections: {asOfSeq, values: {title}}` with the served sequence that set it as
  the watermark, and the follow snapshot carries the same cell. The top-level
  `title` field is gone. The title is read from the whole session log, so a rename
  that landed on any turn is found, and the durable record now reaches the wire as
  `session/title {title, messageSeqs, source:{kind}}` — the shape the fold expects
  — with `source` translated from the durable string to the tagged object
  (`user` for a rename, `fallback` for a title derived from the first prompt).

## Consequences

- Measured live after the fix, reloading mid-answer: the baseline carried
  `revision: 22` with `activeAttempt.nextIndex: 21`, and the stream then delivered
  **21 more chunk frames and the settlement** — no second `start`, no teardown.
  Before the fix the same measurement delivered none.
- Measured over the RPC surface: every `session/list` row now carries
  `projections.values.title` (`{"asOfSeq":3,"values":{"title":"Count from one to
  sixty in words, one"}}`) and no top-level `title`.
- Pinned by tests: the control baseline's exact key set includes `queues`; a
  replayed snapshot's revision is the frames it numbered and the next live frame is
  that plus one; the session list and the follow snapshot both serve the title cell
  with its watermark; and the durable title projects to the tagged wire shape.
- The audit that produced this ADR also established that the authoritative
  reference is the **client the console serves** (upstream `ddefc45`,
  `0.1.6-alpha.2`, checked out at `/tmp/dsh-src`), not the published
  `@deepseek-ai/*@0.1.5-rc.2` artifacts in `node_modules`: five result schemas
  differ between them, and our adapter already implements four of the five newer
  fields. `dsh-console-protocol-recon.md` is corrected accordingly.