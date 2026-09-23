package dshapi

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/internal/dshwire"
	"github.com/feiyu912/zenforge/model"
)

// promptContent is a console prompt flattened into the three things this host
// can act on: the words, the images it carries inline, and the receipts of files
// the console uploaded first.
type promptContent struct {
	text   string
	images []promptImagePart
	files  []promptFilePart
}

// promptImagePart is one inline image, still as bytes: what it is comes from the
// bytes, never from what the part declared.
type promptImagePart struct {
	name string
	data []byte
}

// promptFilePart is one file the console staged, cited by the receipt its upload
// answered with.
type promptFilePart struct {
	receiptID string
	name      string
}

// promptAdmission is what one prompt contributes to the run it starts: the text
// the run is prompted with (with a file's model-visible handle appended), the
// images the run's first request carries, and the transcript blocks the turn's
// projected user message shows.
type promptAdmission struct {
	text        string
	images      []model.Image
	attachments []PromptAttachment
}

// admitPromptAttachments turns a decoded prompt into what the run starts with.
//
// Every part with bytes is stored under the session that submitted it, because
// the console's transcript refers to an attachment by id and reads it back with
// `session/attachment`; an id this host did not publish would be a transcript
// that cannot render. The store is therefore required for an image or a file
// part, and a host without one refuses them with the family's own sentence.
//
// A file is delivered the way the reference host delivers one (dsh-llm
// `fileHandleText`): the bytes are published as a verbatim read-only copy the
// agent's file tools can read, and the model is handed text naming the file, its
// size, its digest prefix and that path. That is the whole of upstream's
// model-visible form for a file block -- there is no binary file input to a model
// here either.
func (h *Handler) admitPromptAttachments(ctx context.Context, sessionID string, content promptContent) (promptAdmission, *methodError) {
	admission := promptAdmission{text: content.text}
	if len(content.images) == 0 && len(content.files) == 0 {
		return admission, nil
	}
	store := h.attachmentsSource()
	if store == nil {
		// The sentence names the store because that is what is missing: without
		// it the transcript could not refer to the attachment it was given.
		return promptAdmission{}, h.attachmentsUnavailable()
	}
	// The handle text a file contributes precedes the prompt's own words, which is
	// the order the console submits in: attachments first, then the text.
	handles := make([]string, 0, len(content.files))
	for _, raw := range content.images {
		descriptor, failure := h.storePromptImage(ctx, store, sessionID, raw)
		if failure != nil {
			return promptAdmission{}, failure
		}
		admission.images = append(admission.images, model.Image{MediaType: descriptor.MediaType, Data: raw.data})
		admission.attachments = append(admission.attachments, PromptAttachment{
			Kind:         "image",
			AttachmentID: descriptor.ID,
			Name:         descriptor.Name,
			MediaType:    descriptor.MediaType,
			Bytes:        descriptor.Bytes,
			Width:        descriptor.Width,
			Height:       descriptor.Height,
		})
	}
	for _, file := range content.files {
		handle, attachment, failure := h.admitPromptFile(ctx, store, sessionID, file)
		if failure != nil {
			return promptAdmission{}, failure
		}
		handles = append(handles, handle)
		admission.attachments = append(admission.attachments, attachment)
	}
	if len(handles) > 0 {
		admission.text = strings.Join(append(handles, content.text), "\n\n")
	}
	return admission, nil
}

// storePromptImage stores one inline image and returns the descriptor the console
// will render, which requires dimensions this host can read.
func (h *Handler) storePromptImage(ctx context.Context, store AttachmentStore, sessionID string, part promptImagePart) (AttachmentDescriptor, *methodError) {
	descriptor, failure := h.storeAttachment(ctx, sessionID, part.name, part.data)
	if failure != nil {
		return AttachmentDescriptor{}, failure
	}
	if descriptor.Width <= 0 || descriptor.Height <= 0 {
		// An image whose header states no size cannot be described to the console
		// -- its message block carries the dimensions a viewer draws at -- so it
		// is refused by name rather than shown as an attachment of unknown size.
		return AttachmentDescriptor{}, fail(attachmentInvalidCode,
			fmt.Sprintf("the image content part's %s header does not state its dimensions, so this host cannot describe it; send a PNG, JPEG, GIF or WebP whose header is intact", descriptor.MediaType),
			map[string]any{"reason": "DIMENSIONS_UNREADABLE", "part": "image", "mediaType": descriptor.MediaType, "attachmentId": descriptor.ID})
	}
	return descriptor, nil
}

// admitPromptFile resolves one staged receipt into the text the model reads and
// the transcript block the console shows.
func (h *Handler) admitPromptFile(ctx context.Context, store AttachmentStore, sessionID string, part promptFilePart) (string, PromptAttachment, *methodError) {
	descriptor, _, err := store.Read(ctx, sessionID, part.receiptID)
	switch {
	case err == nil:
	case errors.Is(err, ErrAttachmentNotFound):
		// The console cites the receipt its upload answered with, so an id this
		// session never staged is the console's own attachment error, named.
		return "", PromptAttachment{}, fail(attachmentInvalidCode,
			fmt.Sprintf("the file part cites receipt %q, which this session has no stored attachment for", part.receiptID),
			map[string]any{"reason": "ATTACHMENT_NOT_FOUND", "part": "file", "receiptId": part.receiptID})
	default:
		return "", PromptAttachment{}, fail(codeInternal, fmt.Sprintf("read staged file: %v", err), nil)
	}
	// A receipt is a file even when its bytes happen to be an image: the console
	// asked for a file, so it is delivered as one, readable at the published path.
	path, err := store.MaterializePromptFile(ctx, sessionID, descriptor.ID)
	if err != nil {
		return "", PromptAttachment{}, fail(codeInternal, fmt.Sprintf("publish the file for the run: %v", err), nil)
	}
	name := descriptor.Name
	if name == "" {
		name = part.name
	}
	if name == "" {
		name = descriptor.ID
	}
	attachment := PromptAttachment{
		Kind:         "file",
		AttachmentID: descriptor.ID,
		Name:         name,
		Bytes:        descriptor.Bytes,
		Path:         path,
	}
	return fileHandleText(name, descriptor.ID, descriptor.Bytes, path), attachment, nil
}

// fileHandleText is the reference host's model-visible form of a file block
// (dsh-llm `fileHandleText`), with this host's published path. It is copied
// rather than paraphrased because a run told less than upstream tells it would
// guess: the digest prefix is there so a later reference to the same file can be
// recognised, and the read-only warning is there because the copy is not the
// operator's working copy.
func fileHandleText(name, attachmentID string, bytes int, path string) string {
	digest := strings.TrimPrefix(attachmentID, "att-")
	if len(digest) > 8 {
		digest = digest[:8]
	}
	identity := fmt.Sprintf("File %q (%d bytes, sha256:%s)", name, bytes, digest)
	if path == "" {
		return fmt.Sprintf("[%s was uploaded, but the current execution environment cannot access a readable path. Report that limitation if its contents are needed; do not claim to have read it.]", identity)
	}
	return fmt.Sprintf("[%s: verbatim read-only copy saved at %q. Read that path with your file tools when its contents are needed; copy it to a writable location before modifying it. When delegating file work, include this saved path in the delegation prompt; only subagents sharing this execution environment can read it.]", identity, path)
}

// recordPromptAttachments publishes what a started run's prompt carried, under
// the run's own id, so the projector of that turn's log can show it. A store
// failure is reported but never fails the prompt: the run is already answering,
// and a transcript that cannot show an attachment is a smaller loss than a turn
// that never started.
func (h *Handler) recordPromptAttachments(ctx context.Context, sessionID, runID string, attachments []PromptAttachment) {
	if len(attachments) == 0 {
		return
	}
	store := h.attachmentsSource()
	if store == nil {
		return
	}
	_ = store.RecordPromptAttachments(ctx, sessionID, runID, attachments)
}

// promptInputs is the same seam for this package's own projections: the console's
// page and a session list row are served from the log, and their user messages
// carry the prompt's attachments just as the live stream's do.
func (h *Handler) promptInputs(ctx context.Context) dshwire.Inputs {
	store := h.attachmentsSource()
	if store == nil {
		return nil
	}
	return PromptInputAttachments(func(runID string) []PromptAttachment {
		recorded, err := store.PromptAttachments(ctx, runID)
		if err != nil {
			return nil
		}
		return recorded
	})
}

// PromptInputAttachments is the projector's half of the same seam: the console's
// follow stream asks it what one run's prompt carried, by run id. It is exported
// because the mount that installs the store is the only place that knows both
// halves, and this is what it hands the stream.
func PromptInputAttachments(source func(runID string) []PromptAttachment) dshwire.Inputs {
	if source == nil {
		return nil
	}
	return func(runID string) []dshwire.Attachment {
		recorded := source(runID)
		if len(recorded) == 0 {
			return nil
		}
		attachments := make([]dshwire.Attachment, 0, len(recorded))
		for _, attachment := range recorded {
			attachments = append(attachments, dshwire.Attachment{
				Kind:         attachment.Kind,
				AttachmentID: attachment.AttachmentID,
				Name:         attachment.Name,
				MediaType:    attachment.MediaType,
				Bytes:        attachment.Bytes,
				Width:        attachment.Width,
				Height:       attachment.Height,
			})
		}
		return attachments
	}
}

// attachmentPartName names which kind of part a refusal is about, so a client can
// tell an image from a file without parsing the sentence.
func attachmentPartName(images []promptImagePart, files []promptFilePart) string {
	if len(images) > 0 {
		return "image"
	}
	if len(files) > 0 {
		return "file"
	}
	return ""
}

// promptTextMeta carries the operator's own words into the run's metadata, which
// is where the session title reads them from: a prompt whose file was delivered
// as text the model reads must not title the conversation with that text.
func promptTextMeta(text string) map[string]any {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return map[string]any{zenforge.MetaPromptText: text}
}
