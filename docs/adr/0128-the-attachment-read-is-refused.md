# ADR 0128: The Attachment Read Is Refused, Because the Write Half Does Not Exist

Status: accepted

## Context

`session/attachment` is the read half of the console's attachment flow: a caller
holding an attachment id asks for the picture back, and the declared result is
the metadata the transcript renders (`attachmentId`, a `mediaType` among png,
jpeg, webp and gif, `bytes`, `width`, `height`, an optional `name`, optional
original dimensions) **and** `data`, the encoded bytes
(`api/session-controller/lib/types.d.ts`). It is how a historical image in a
transcript is loaded.

The write half does not exist on this host, and it was already refused:

- `session/prompt` refuses a prompt whose content carries an image or file part
  by name ("prompt content part \"image\" is not supported: this host accepts text
  parts only"), because the run manager has no attachment intake and accepting the
  bytes while dropping them would be a lie the operator discovers later;
- the command surface refuses submitted attachments for the same reason;
- `fileUploads/upload` is not served at all, so there is no route by which an
  attachment id could come into existence here.

Three answers were available for the read: a 404 (the method stays invisible in
the ledger), a not-found for an id that can never exist, or a refusal by name. The
first two both mislead: a 404 says "this host never heard of it" while the panel
that asks exists, and `not-found` says "that attachment is gone" when the truth is
that no attachment has ever been here.

## Decision

- **Refuse `session/attachment` by name**, with `unimplemented`, the missing
  capability in the details (`an attachment store`), and one sentence naming both
  the reason and the substitute: this host has no attachment store, a prompt's
  image and file parts are refused when they are submitted, and a file put in the
  workspace can be read by the agent's own file tools. The sentence is written once
  so the refusal cannot drift from the half that already refuses.
- **Validate the declared fields first.** The method's own `sessionId` and
  `attachmentId` arrive flattened from the request object, and a field the method
  does not declare is still reported as the typo it is; a well-formed request is
  refused because the *store* is missing, not the argument.
- **Do not build the store yet.** Serving attachments is a subsystem rather than a
  route: it needs the upload path (`fileUploads/upload`, still unserved), a store
  holding the bytes and the image metadata, and a prompt path that carries a
  reference into the run -- and the reference implements it as three packages
  (`dsh-attachment`, `dsh-attachment-local`, `client/ui-attachment`). Refusing the
  read now keeps both halves of the seam consistent and leaves the ledger saying
  what is actually missing. The association of a returned id with a session
  (`sessionId` in the request) is part of that subsystem, not of this refusal.
- **Record the consistency in the ledger and in the limitations.** The refused
  table gains the row, its counts move to 46 served / 4 streams / 17 refused / 42
  unserved, and `docs/limitations.md` says plainly that the composer's attach
  affordance cannot work and what to do instead. The same file's older bullet that
  listed "search, attachments" among the bare-404 namespaces is corrected: search
  is served (ADR 0127) and attachments are refused by name.

## Consequences

- `TestSessionAttachmentEnvelopeMatchesTheVendoredConsole` pins the wire shape
  against the vendored bundle -- one `request` object holding `sessionId` and
  `attachmentId`, a result of `attachment` and `data`, the image metadata the
  console reads, and the handler's accepted names.
  `TestSessionAttachmentNamesTheMissingCapability` refuses both the flat form and
  the request splice, asserts the capability detail and the named substitute, and
  keeps the typo rule. `TestPromptRefusesTheSameAttachmentHalf` asserts the other
  half of the seam: a prompt carrying an image part answers
  `session/unsupported-content` with the text-only rule.
- The next-up list narrows to `session/fork` and `session/updateQueue`, with the
  fork's size recorded: the reference seeds a child session with the parent's
  events cut at a completed turn boundary
  (`agents.create({sessionId, seed, inheritedEventCount})`) and fails with
  `session/fork-unavailable` when no completed turn contains the requested
  sequence -- a seeded-log mechanism this host does not have, which is why forking
  is a chain of its own rather than a route added beside this one.
- Live on a scratch host: `session/attachment` with a flat request and with the
  request splice answered the same `unimplemented` refusal naming the capability;
  an extra field answered `gateway/arguments-invalid`; a prompt carrying an image
  part answered `session/unsupported-content`; and `fileUploads/upload` answered
  `404`, which is the write half's absence.