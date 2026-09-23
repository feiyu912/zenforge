# 0139. Every recorded attachment deviation is closed: WebP is measured, the transcript shows the prompt's attachments, and a file is delivered as upstream delivers it

- Status: Accepted
- Date: 2026-09-22
- Related: 0138 (the store, the two upload routes, the image path), 0137 (the
  verification recipe), 0099 (the console boundary), 0120 (the workspace file
  face)

## Context

ADR 0138 shipped the console's attachment family and recorded four deviations
instead of hiding them. Three of them were capability gaps the console could see,
and this ADR closes them:

1. the projected durable `user/message` carried the prompt's **text only**, so
   once the console retires its local echo the transcript showed a prompt whose
   image the model had been given;
2. a **WebP** could be stored and sent but not read back, because the console's
   attachment descriptor requires pixel dimensions and this host linked no WebP
   header reader -- it refused with `DIMENSIONS_UNREADABLE` naming `image/webp`;
3. a **file** part was refused, because the model path carried images only.

The third turned out not to be a gap in the model at all. Reading the reference
host's own conversion (`@deepseek-ai/dsh-llm`, `fileHandleText` and
`replaceFilesWithHandles`) shows that upstream never sends a file's bytes to a
model either: it replaces the file block with **text** naming the file, its size,
its digest prefix and the path of a verbatim read-only copy the agent's file tools
can read. There is no file input to the model to be missing. The words upstream
uses are pinned in the code this host copies:

```
[File "notes.txt" (16 bytes, sha256:41636006): verbatim read-only copy saved at
"<path>". Read that path with your file tools when its contents are needed; copy
it to a writable location before modifying it. When delegating file work, include
this saved path in the delegation prompt; only subagents sharing this execution
environment can read it.]
```

with a second sentence for a host that cannot publish a readable path ("was
uploaded, but the current execution environment cannot access a readable path.
Report that limitation if its contents are needed; do not claim to have read
it.").

## Decision

### 1. A WebP is measured from its own header

`model.ImageSize` now reads four image formats. The standard library covers PNG,
JPEG and GIF; WebP is read by `imageSizeWebP`, a bounded walk of the RIFF chunk
list that understands all three bitstreams: `VP8X` (24-bit canvas, minus one, after
its flags and reserved bytes), `VP8L` (two 14-bit fields packed into one 32-bit
word) and `VP8 ` (the frame header's 16-bit dimensions, masked to 14 bits). It
reads at most 64 bytes of chunks, never pixels, and refuses what does not add up --
a short RIFF header, a chunk length past the end of the file, a missing bitstream
signature, a zero dimension -- with `ErrImageDimensionsUnreadable`.

It is checked against **real files**, not headers written by the test: five
embedded WebP files (lossy, wide lossy, lossless, extended with EXIF, 640x480 with
alpha) produced by Pillow, plus a constructed `VP8X`-only header so the canvas
branch is pinned by a case whose frame cannot supply the answer. Twenty generated
files across four container variants and five sizes (including 1x1 and 640x480)
were additionally compared against Pillow's own reported size while implementing
this: all twenty agreed.

`session/attachment` therefore serves a WebP's descriptor with its real
dimensions, and the `DIMENSIONS_UNREADABLE` refusal now means what it says -- a
header this host cannot read (a truncated or corrupt file), not a format it does
not support.

### 2. The projected user message carries what the model was given

`run.started` holds the prompt's text and identity durably; it has no framework
metadata (verified live in ADR 0138), and inventing a second `user/message` for
the same turn would append a second message, because every projected console
record is a surface **append** -- the console's store does not upsert messages.
The attachments therefore reach the projector by a seam:

- the host that admits the prompt stores it (`dshapi.AttachmentStore.Save`) and
  records the transcript blocks against the run the turn is
  (`RecordPromptAttachments`, one record per run, under the attachment store's own
  tree -- `attachments/prompts/<digest-of-run>.json`, mode 0600);
- `dshwire` gains an `Inputs` provider (`func(runID string) []Attachment`), and
  the `run.started` arm builds the message content as **the attachments in the
  order the console submitted them, then the text**. That is the console's own
  order (`ui-conversation`'s `serializeAttachments`: attachments first);
- both readers of the log use it: the live follow stream (through
  `dshstream.Config.InputAttachments`) and this package's own `session/page` and
  session-list projections (through the store's `PromptAttachments`).

A descriptor that cannot be rendered is dropped rather than shown wrong: an image
block needs its stored id, media type and positive dimensions, and a
`session/prompt` whose image header states no size is refused by name at
admission -- the same `session/attachment-invalid` with
`reason: DIMENSIONS_UNREADABLE`. A file block carries the stored id, name and
size.

### 3. A file is delivered as upstream delivers it

`session/prompt`'s `file` part is no longer a refusal. The receipt it cites is
resolved from the store (`Read`), and:

- the model is handed the handle text above, appended **before** the prompt's own
  words (attachments first, upstream's order);
- the store publishes a verbatim copy at
  `<workspace>/.zenforge/attachments/<digest12>-<sanitized name>`, mode 0444,
  atomically and idempotently, and the run is told that path. It has to be inside
  the workspace: the agent's own file tools are rooted there (`workspace/local`
  opens a root and refuses paths outside it), so a copy anywhere else would be a
  path the model is told to read and cannot;
- the transcript block names the stored attachment (`{type: "file", attachment:
  {attachmentId, name, bytes}}`);
- a receipt this session never staged is the console's own
  `session/attachment-invalid` with `reason: ATTACHMENT_NOT_FOUND`, and a `file`
  part with no `receiptId` at all is a malformed part
  (`gateway/arguments-invalid`).

A host with no workspace publishes nothing and says so in the model's handle text,
which is upstream's own alternate sentence -- not a silent path to nowhere.

The handle text is part of the run's **input**, which is what the session title is
derived from. The live host showed the consequence immediately: the console's
sidebar titled the conversation
`[File "notes.txt" (16 bytes, sha256:41636006): verbatim read-onl`. The operator's
own words therefore travel beside the model's input in the run's metadata
(`zenforge.MetaPromptText`), and `sessionTitleInput` reads them first -- the same
shape the plan-execute preset already uses to keep its stage prompt out of a
title (ADR 0106). The live host now titles that conversation
`what colour is this, and what does the`.

### 4. What remains a refusal, and why it is not a deviation

- A prompt part that is neither text, image nor file is
  `session/unsupported-content` naming itself.
- An image or file part into a run that is **already answering** is refused by
  name: this host's queue and steer paths carry the next turn's text, so the
  attachment would be dropped silently. (Upstream's queue carries content blocks;
  this host's does not, and the refusal says which half is missing rather than
  pretending.)
- `session/attachment` answers **images only**, because the pinned client's
  descriptor type is a four-value image union: a non-image read has no shape to
  answer in, and the console never asks for one -- a file's transcript block is
  drawn from its handle text and its stored bytes are read by the agent's file
  tools.
- The store's own ceilings stay: 64 MiB per attachment, 8 MiB more on the unary
  route from the console envelope limit, and `model.MaxImageBytes` per image and
  per message.

## Consequences

- The three deviations ADR 0138 recorded are closed. The ledger's `session/prompt`
  row now records text, image and file parts as served, with the live-run case
  refused by name.
- `zenforge.Task`, `RunState` and every existing durable event are **unchanged**:
  the prompt's attachments are a projection input the host supplies, not a new
  log field. A log projected without the provider -- an older host reading a newer
  log, or a test -- shows the prompt as text only, which is exactly what it
  showed before.
- One host-writable path exists inside the workspace:
  `.zenforge/attachments/`. It is read-only by mode, content-addressed by name,
  and created only when a prompt actually carries a file.
- A run's attachments are recorded once, after the run starts. If the host dies
  between `Start` and `RecordPromptAttachments`, the turn's transcript shows the
  prompt's text without its attachments: the record is best-effort by design,
  because the run is already answering and failing the prompt would be a bigger
  loss. `docs/limitations.md` says so.

## Verification

- `go test ./internal/dshwire/` pins the projected message: attachments before the
  text, an image block's dimensions and name, a file block's size, a nameless
  image rendered without a name, a descriptor that cannot be rendered dropped, and
  an ordinary turn still one text block.
- `go test ./model/` pins the four formats: real embedded WebP files of all three
  bitstreams, the `VP8X`-only canvas case, nine unreadable-header cases, and the
  header bound.
- `go test ./internal/dshapi/` pins admission and the seam: a file delivered as
  upstream's handle text before the words, an unknown receipt named, a receiptless
  file part malformed, an image that cannot be measured refused, a host without a
  store refusing with the family's sentence and starting no turn, and the
  recorded attachments reaching the projector's inputs by run id.
- `go test ./cli/` pins the store's new halves: the prompt record's round trip and
  privacy, one run's record not being another's, the read-only published copy
  inside the workspace with the digest name, publishing twice, another session's
  id refused, a host with no workspace publishing nothing, and a name that cannot
  escape its directory.
- `go test .` (the root package) pins the title: the operator's words win over an
  input that carries a file handle, the run's input is still the fallback, and the
  plan-execute stage's own copy still wins over both.
- Live on a real host (a scripted OpenAI-compatible endpoint on its own port,
  logging every request body): a WebP uploaded through the raw route and read back
  with `session/attachment` answered `{"mediaType": "image/webp", "width": 4,
  "height": 3, "bytes": 36}` with the bytes round-tripping exactly; one prompt
  carrying an inline PNG and a staged `notes.txt` was accepted, and the console's
  **own** `user/message` record (read back through `session/page`) carried the
  image block (`width: 9, height: 5`, the PNG's real size), the file block, and the
  handle text; the endpoint's request log shows the model receiving an `image_url`
  data URI carrying the PNG **and** the handle text naming
  `.zenforge/attachments/41636006b744-notes.txt`; that published copy is inside the
  workspace, mode `0444`, and holds exactly the uploaded bytes; and the session's
  title is the operator's words.