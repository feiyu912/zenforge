# 0138. The console's attachments are stored, images reach the model, and files are refused for the reason they cannot

- Status: Accepted
- Date: 2026-09-22
- Related: 0099 (the console boundary), 0120 (the workspace file face), 0134
  (the `@` menu over the same directory), 0128 (refusing a family by name)

## Context

The served console stages `client/file-upload` and `client/ui-conversation`, so
the composer's paperclip is live UI with no host behind it: `fileUploads/upload`
and `session/attachment` answered `unimplemented` with one sentence -- "this host
has no attachment store" -- and `session/prompt` refused every non-text content
part, whatever it was. The ledger carried all three as refused or text-only.

Reading the pinned client's own send path (not the descriptor table, which
describes a different direction) settled what the console actually does:

- an **image** is never uploaded. `ui-conversation`'s `encodeImage` puts it in the
  prompt itself: `{type: "image", mediaType, data: <base64>, name?}`, with the
  media type restricted to PNG, JPEG, WebP or GIF exactly as the four-value
  descriptor union says;
- a **non-image file** is uploaded first and the prompt cites only the receipt:
  `{type: "file", receiptId}`;
- the upload has **two entry points** on the same service. The bundle posts a
  `Blob`/stream to `/api/session/uploadFileBinary?sessionId=&name=` with an
  `application/octet-stream` body and reads `{ok, value|error}` from it
  (`parseFileUploadResult`); when it holds bytes instead it calls the RPC
  `fileUploads/upload` with `{data: base64, name?}` and reads the unary result.
  Both answers are `{receiptId, file: {attachmentId, name, bytes}}`;
- `session/attachment` is the **read** half, and its result schema is an *image*
  descriptor: `mediaType` is the image union and `width`/`height` are required.

The reference host's own behavior (`dsh-api-session-controller/lib/index.js`,
`dsh-attachment`) confirms the split: a prompt's file receipt is resolved against
the upload store and an un-staged receipt is `session/attachment-invalid`
`FILE_NOT_STAGED`; an image is checked against the selected model's input
modalities (`MODEL_DOES_NOT_SUPPORT_IMAGES`); and `admitPromptContent` turns an
inline image into a durable reference.

This host's model path already carries images: `model.Message.Images`,
`model.Image{MediaType, Data, Path}`, `model.MaxImageBytes`,
`model.SupportedImageMediaType`, and both adapters render them. `tools/viewimage`
already sniffed a media type from bytes. So the missing pieces were a store, the
two upload routes, dimensions, and an admission path -- not model support.

## Decision

**Serve the attachment family where the machinery can carry it, and refuse by
name exactly where it cannot.**

1. **One store, two routes.** `internal/dshapi.AttachmentStore` is the seam;
   `cli/attachments.go` is the disk implementation under
   `<checkpoint-dir>/attachments`. Bytes are content-addressed
   (`objects/<sha256>`, so the same file uploaded twice is one object) and written
   atomically with mode 0600; the descriptor is a per-session sidecar under
   `sessions/<sha256(sessionID)[:16]>/<id>.json`, so a name one session gave a file
   is invisible to another and a guessed id resolves to that session's own record
   or to not-found. The id is a 32-character digest prefix, and a read re-hashes
   the bytes and refuses a mismatch rather than publishing a descriptor that
   describes different content. Both upload routes (`fileUploads/upload` and the
   raw-byte path, which `Handler.ServeHTTP` dispatches before envelope parsing
   because its body is not JSON) write through one function, so they cannot
   disagree about what was stored. A nil store still answers the family's original
   sentence, because a host with no store still has no attachment to read.

2. **The media type comes from the bytes, never from the name**, via a new
   `model.DetectImageMediaType` (which `tools/viewimage` now shares, so the tool's
   accepted types and the console's stored types cannot diverge). Dimensions come
   from `model.ImageSize`, which reads the header with `image.DecodeConfig`
   (PNG/JPEG/GIF) and reports `ErrImageDimensionsUnreadable` for anything else.

3. **A WebP is stored and named, not guessed.** Its bytes are real and
   `session/attachment`'s descriptor requires dimensions this host cannot read, so
   the read is refused with `session/attachment-invalid`,
   `reason: DIMENSIONS_UNREADABLE`, naming `image/webp` -- the same code the
   console already declares for an unusable attachment. Adding a WebP header
   reader is the obvious follow-up; guessing `0x0` was not an option.

4. **An image prompt is delivered to the model.** `session/prompt`'s content
   decoder now reads `image` parts: base64 decoded, media type *checked against
   the bytes* (a part that declares PNG and carries text is refused rather than
   sent to a provider as an image), and bounded by `model.MaxImageBytes` per image
   and per message -- the aggregate bound exists because every later request of
   the conversation replays them. The images ride on the run's own input message
   through a new `zenforge.Task.Images` field, which `newTaskRunState` attaches to
   the message it appends, in the metadata slot tool results already use
   (`metaMessageImages`). That is the only framework change, and it is why the
   images do not go in `InitialMessages`: the run appends its input message itself,
   so a caller that put them there would produce two consecutive user messages,
   which some providers reject and the rest read in the wrong order.

5. **A file part is refused for the reason it cannot be carried.** The console's
   `{type: "file", receiptId}` is answered with
   `session/unsupported-content`: this host can store the file and hand its bytes
   back by id, but its model path carries images only, so there is nothing to
   deliver the file to, and the workspace remains the way to show the agent a
   file. That supersedes the old "no attachment store" sentence, which would now
   be false.

6. **An image into a run that is already answering is refused by name.** The
   queue and steer paths carry the next turn's *text*
   (`RunManager.Steer` takes a string, and a queued row's projection is text), so
   an image sent `mode: "queue"` while a run is live is refused with
   `session/unsupported-content` naming the queue, rather than silently dropped at
   the boundary. An image with `mode: "steer"` is refused by the same rule, since
   a steer is a text message the running turn lifts.

## Consequences

- The ledger moves two rows: `fileUploads/upload` and `session/attachment` are
  served, and the summary is now 58 served / 4 streams / 44 refused / 0 unserved
  of 106. `session/prompt`'s row records the image admission and both refusals.
- The console's upload flow is truthful end to end: a picked file uploads through
  whichever route the bundle chose, gets a receipt, and appears as a chip. Sending
  it is refused with a sentence that says what is missing and what to do instead.
- An image prompt reaches the model, and the answer is about the image.

### Deviations, recorded rather than hidden

1. **The durable transcript does not yet carry the prompt's image.** The projected
   `user/message` is built from `run.started`'s `input` (`dshwire.userMessage`,
   one text block), so after the console retires its local echo the transcript
   shows the prompt's text without an image block, even though the model received
   the image. The console's echo shows the image while the turn starts, and the
   stored descriptor is what would let the message render it. Fixing this needs a
   seam from the adapter to the projector: the run's `Task.Meta` is checkpointed
   with the run, but `run.started`'s payload deliberately carries no framework
   meta, and `dshwire.New` takes only an identity. The next chain chooses between
   a narrow projector input (a run-metadata provider) and a host-authored durable
   event -- and this chain deliberately did *not* add an unconsumed payload field
   for it: an earlier draft put `Task.Meta` into `run.started` and was reverted
   when nothing read it, because a log field with no reader is a promise the code
   does not keep.
2. **`session/attachment` answers images only**, because its vendored result
   schema is an image descriptor; a stored non-image is refused with
   `NOT_AN_IMAGE`. That matches the console, which only reads back images.
3. **A WebP cannot be read back** (decision 3). It can still be *sent* in a
   prompt, which needs no dimensions.
4. The upload size ceiling is this host's own `attachmentMaxBytes` (64 MiB); the
   unary route is additionally bounded by the console envelope limit (8 MiB), so
   the raw route is the one that can carry a large file.