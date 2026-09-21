# ADR 0120: The File Surfaces the Console Reads

Status: accepted

## Context

A file-by-file audit of the host against the console's own plugins found three
deviations in the workspace-file and forwarded-event surfaces. One of them was
proved with the upstream code, not inferred.

1. **`workspaceFiles/changes` had no stream route.** Upstream declares it as one of
   its four streams, and the shipped console's file provider opens it *before* it
   stats anything:
   `const notices = changes.follow(sessionId, signal); if (!await notices.ready) return;`
   A stream that fails resolves that flag to false, and the resource record stays
   `status: "loading"` forever — so clicking any `@file` reference opened a tab that
   never painted.
2. **`workspaceFiles/readAll` returned the text arm.** Upstream's is
   `Promise<WorkspaceFileBytes>`; a consumer that previews a document decodes
   `data` as base64 and fails with "malformed base64 data" when it is absent.
3. **A failed `$events/result` call tore down the whole forwarded-event stream.**
   Upstream answers success for an answer it cannot use — an unknown or already
   settled `eventId`, a delegating `{kind:"next"}`, a listener failure
   `{kind:"rejected"}` — while this host refused all three. The client treats a
   failed result call as a generation failure, reconnects, and re-delivers every
   pending waterfall; the shipped approval panel sends `next` whenever it cannot
   scope the owning session, so this was reachable on every approval raised by a
   background turn.

The same audit also proved a live defect in ADR 0118's own code by running
upstream's real `expandAssistantStream` over this host's output: the compact prefix
of a block with exactly **one** delta marshalled `dt: null`, and the console's
validator throws `TypeError: text-chunks dt must contain safe integers`. The
baseline is expanded on the stream's opening path, so a mid-answer reconnect whose
prefix held a short reasoning block or a single first delta died terminally instead
of resuming.

## Decision

- **The file-watch subscription is served**: `{kind:"ready"}` once the scope is
  present, then the stream stays open until the client leaves. This host watches no
  files, so no `change` frame is ever sent: the file renders, and a file rewritten
  afterwards keeps showing its version at open time until the tab is reopened.
  Sending change frames would require a version token this host cannot derive
  honestly, and a fabricated one would make the console repaint on a lie.
- **The scope is checked for presence, not for existence.** Upstream's scope is a
  session, which exists from the moment a conversation is created; this host's only
  existence test is whether a turn has started, so refusing an unknown scope here
  would break the file tree in a conversation that has not been prompted yet.
- **`readAll` is the bytes arm**, refusing a file over the complete-file limit with
  `workspace-file/too-large {path,limit}` rather than truncating it.
- **`$events/result` fails only for a genuinely malformed call or an unknown
  clientId** (upstream's own `gateway/internal`). An unusable answer is a success
  that decides nothing; the request stays pending and can still be answered.
- **The compact prefix always writes `dt` as an array**, empty for a one-delta run.
- **A stream endpoint no method exports answers `gateway/invocation-unavailable`**,
  upstream's own code, instead of an invented `gateway/not-found`.
- **The settlement binder mirrors the client's predicate**: an
  `assistant/message` settlement must be on the append surface and carry the
  attempt's own turn and step, and its sequence must be past the attempt's opening
  sequence. A looser binder would release an attempt with a sequence the console
  never staged, which it answers with a rebaseline.

## Consequences

- Verified against upstream's own expander (extracted from the shipped bundle):
  `dt: []` expands to a frame, `dt: null` throws the validator's `TypeError`. The
  regression is pinned by a socket test that asserts the decoded `dt` is an array.
- A one-delta run is now byte-identical in shape to upstream's builder, and the
  settled-message path (which already allocated its gap list) stays as it was.
- An approval raised by a background turn no longer resets the approvals channel,
  and a stale double-click no longer costs the stream.
- Pinned by tests: the watch stream's `ready` frame and its refusal of an empty
  scope; `readAll`'s base64 payload; the four forwarded-event answers that are now
  no-ops; the unknown-endpoint code; and the stricter settlement binder (which the
  existing unit fixtures had to be made faithful to, by carrying `turn` and the
  append surface).
- Still missing, and stated as such: no `change` frames (a tab does not live-update),
  no `readRelated`, and the file surfaces still page by whole file rather than by
  page for the text arm.