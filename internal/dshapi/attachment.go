package dshapi

import (
	"context"
	"encoding/json"
)

// `session/attachment` is the read half of the console's attachment flow: a
// caller that already holds an attachment id asks the host for its bytes and its
// image metadata (api-session-controller/lib/types.d.ts: an attachment carries
// `attachmentId`, `mediaType` among png/jpeg/webp/gif, `bytes`, `width`,
// `height`, an optional `name` and the original dimensions).
//
// This host has no attachment store and no way to fill one, which is why the
// *other* half already refuses: `session/prompt` refuses a prompt whose content
// carries an image or file part, by name, because the run manager has no
// attachment intake and dropping the bytes would be a lie discovered later
// (`decodePromptContent`), and the command surface refuses submitted attachments
// for the same reason. The console's own upload route, `fileUploads/upload`, is
// not served either, so no id can come into existence here to be read back.
// Refusing the read by name keeps the two halves consistent and tells the caller
// what is actually missing instead of answering a not-found for an id that could
// never have existed (ADR 0128).
//
// Reading a workspace file is the honest substitute and it already works: a file
// the operator puts in the workspace can be read by the agent's own file tools,
// which is what the message points at.

// attachmentRefusal is the one sentence this family refuses with, so the reason
// and the substitute cannot drift from the halves that already refuse.
const attachmentRefusal = "this host has no attachment store: a prompt's image and file parts are refused when they are submitted, so there is no attachment to read; put the file in the workspace and ask the agent to read it"

// sessionAttachment refuses the read half of the attachment seam by name. Its two
// declared fields are still validated, so a typo is reported as a typo rather
// than as this host's missing store; a well-formed request is refused because the
// store is what is missing, not the argument.
func (h *Handler) sessionAttachment(_ context.Context, args map[string]json.RawMessage) (any, *methodError) {
	if failure := rejectUnknownArguments(args, "sessionId", "attachmentId"); failure != nil {
		return nil, failure
	}
	return nil, fail(codeUnimplemented, attachmentRefusal,
		map[string]any{"capability": "an attachment store"})
}
