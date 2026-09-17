# ADR 0050: Images, And Reasoning Replayed With Its Signature

Status: accepted

## Context

Two provider capabilities were missing, and they fail in opposite ways when
handled lazily.

An **image** the agent cannot see produces a confident answer about something
else. The reference has a `view_image` tool for exactly this: the user points
at a screenshot, and the model has to actually look at it.

**Reasoning** is provider state that must be returned verbatim. A provider
that streams signed thinking blocks rejects a request that omits the block or
its signature, so "keep the reasoning for logs but do not send it back" is not
an option for those providers; and folding reasoning text into the answer
silently corrupts the answer.

## Decision

### An image is message content, not tool-result text

`model.Message.Images` carries images beside the text. A message with images
is serialized as multipart content (OpenAI `image_url` data URLs, Anthropic
base64 `image` blocks); a text-only message keeps the plain string form, which
is what providers and proxies expect in the common case.

`view_image` produces the image on the **tool result's metadata** through the
`tools.MetadataCarrier` interface, and the agent copies it onto the message it
appends for that tool call. The model-visible tool result stays text: the
bytes belong in the message, and repeating them in the result JSON would
double the cost of every later request in the conversation.

### Media type comes from the bytes

`view_image` sniffs PNG, JPEG, GIF, and WebP from magic bytes rather than
trusting the extension, and refuses anything else. A mislabelled file must not
be sent as a format the provider will reject, and a renamed executable must
not be treated as an image. The accepted set is the intersection of what every
supported provider takes, so a run cannot come to depend on one vendor's
format. The tool is also a normal workspace tool: the path goes through the
same confinement as every file tool, and an escaping path is refused by the
workspace rather than by the tool's own logic.

### Reasoning travels in message metadata, with its signature

`model.Message.Reasoning` and `ReasoningSignature` capture the provider's
reasoning and its signature. They are stored in `MessageState.Meta` (a field
the run-state schema already defines, so `zenforge.run_state.v1` is
unchanged), which means they survive a checkpoint and are replayed on the next
turn. Anthropic replays a `thinking` block first, verbatim and with its
signature; OpenAI reasoning is captured and **not** replayed, because the
chat-completions API rejects an assistant `reasoning` field, and sending a
field a provider does not accept is worse than not sending it.

Reasoning without a signature is captured but not replayed: a half block
would be rejected, and the text is still in the transcript.

### Reasoning is never answer text

Reasoning streams on its own event (`model.reasoning`), separate from
`model.delta`, and never enters the answer draft. Mixing it in would both
corrupt the answer and re-send reasoning as an assistant message. The durable
attempt draft is deliberately not extended with a reasoning field: the schema
is frozen, and a turn interrupted mid-stream loses its reasoning anyway,
because the signature only arrives when the block ends.

## Consequences

Benefits:

- an image in the workspace can be shown to the model and is replayed on
  every later turn in the conversation, including after a resume;
- the image is confined and bounded like any other file read, and its format
  is verified rather than assumed;
- signed thinking blocks round trip correctly, so providers that require
  them work instead of failing with an opaque API error;
- reasoning is visible in the event stream without ever being mistaken for
  the answer;
- no run-state or checkpoint schema change was needed, so old checkpoints
  keep loading (and the reader accepts both the tagged and the default field
  spellings).

Costs and limits:

- an image is re-sent on every subsequent request, so a long conversation
  with many images costs tokens each turn; there is no automatic pruning of
  older images (compaction treats them as message metadata);
- only PNG, JPEG, GIF, and WebP are accepted, with no PDF or SVG support;
- `view_image` has no region/zoom, no OCR, and no URL fetching: a web image
  must be downloaded into the workspace first;
- OpenAI reasoning replay is not implemented (chat completions cannot
  express it), so only the transcript and the event stream benefit there;
- a reasoning turn interrupted mid-stream loses its reasoning on resume, and
  there is no reasoning-effort or summary control on the adapters.

## Alternatives Rejected

### Put The Image In The Tool Result Text As Base64

Every later request would carry the bytes twice, and the model would receive
the image as text it cannot see. It also breaks the invariant that a tool
result is something the model reads.

### Trust The File Extension

A `.png` that is not a PNG becomes a provider error at best and an
unexpected payload at worst; sniffing the bytes is cheap and makes the
failure local and legible.

### Fold Reasoning Into The Assistant Content

The transcript would look complete, and the answer would be wrong: a
reasoning-heavy turn would present its private reasoning as its reply, and
the next request would re-send it as an assistant message.

### Add Reasoning To The Persisted Model Attempt

The durable attempt is part of a frozen schema, and extending it would
require a version bump for state that cannot be replayed before the block
ends anyway. Message metadata carries it with no schema change.

### Replay Reasoning To OpenAI

The chat-completions API has no accepted assistant reasoning field; sending
one risks a rejected request for no benefit. The transcript and event stream
retain it, and the gap is recorded rather than guessed at.
