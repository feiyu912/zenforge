package dshapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/feiyu912/zenforge/model"
)

// The console's attachment flow has two halves, and this file serves both.
//
// `fileUploads/upload` is the RPC half: the console hands over bytes (base64 in
// `request.data`) and gets back `{receiptId, file: {attachmentId, name, bytes}}`.
// It also has a second, raw-byte entry point on the same service -- the staged
// `client/file-upload` bundle posts a Blob or a stream to
// `/api/session/uploadFileBinary?sessionId=&name=` with an
// `application/octet-stream` body, because base64 would inflate the bytes it
// already holds -- and the answer there is the same `{ok, value}` object the
// unary envelope carries, parsed by the same client code
// (`parseFileUploadResult`). Both routes land in one store through
// `storeAttachment`, so the two cannot disagree about what was stored.
//
// `session/attachment` is the read half: `{sessionId, attachmentId}` in,
// `{attachment: {attachmentId, mediaType, bytes, width, height, name?}, data}`
// out, with `data` base64 (the client codec decodes it to bytes). Its result
// schema is an *image* descriptor -- `mediaType` is the four-value image union,
// and `width`/`height` are required -- so an id whose bytes are not an image, or
// whose image header this host cannot read, is refused by name rather than
// described with dimensions that would be a guess (ADR 0138).
//
// `session/prompt` is the third place attachments appear: an image part travels
// inline (`{type: "image", mediaType, data}`), and it is admitted here because
// the model path already carries images (`model.Image`, and both adapters render
// them). A `file` part cites a receipt (`{type: "file", receiptId}`) and is
// refused by name: this host can store the bytes and hand them back by id, but
// its model path carries images only, so there is nothing to deliver the file
// *to*.

// rawFileUploadPath is the console's raw-byte upload route, exactly as the
// staged `client/file-upload` bundle builds it (`FILE_UPLOAD_PATH`). It is a
// path on this host's own API surface, not a console RPC, so it is matched here
// rather than dispatched by name.
const rawFileUploadPath = "/api/session/uploadFileBinary"

// AttachmentDescriptor describes one stored attachment without its bytes. Width
// and Height are the decoded pixel dimensions for an image whose header this host
// can read, and zero otherwise (a non-image, or a WebP).
type AttachmentDescriptor struct {
	ID        string `json:"attachmentId"`
	Name      string `json:"name"`
	MediaType string `json:"mediaType"`
	Bytes     int    `json:"bytes"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
}

// AttachmentStore is the durable seam behind both upload routes and the read
// method. It is an interface rather than a directory walk inside this package
// because only the CLI knows where the host may write (the checkpoint directory),
// and a nil store leaves the whole family answering `unimplemented` with the
// dependency named.
type AttachmentStore interface {
	// Save stores bytes for one session, under an optional display name, and
	// returns their descriptor. The bytes are stored verbatim: the media type is
	// what a sniffer finds in them, never what a name claims.
	Save(ctx context.Context, sessionID, name string, data []byte) (AttachmentDescriptor, error)
	// Read returns a stored attachment's descriptor and its exact bytes. A
	// descriptor that is not published for that session is reported as
	// [ErrAttachmentNotFound].
	Read(ctx context.Context, sessionID, attachmentID string) (AttachmentDescriptor, []byte, error)
	// RecordPromptAttachments publishes what one turn's prompt carried, under the
	// run id that turn runs as, so the console's transcript of that turn can show
	// it (ADR 0139). It is the write half of the seam the projector reads: the
	// durable log's run.started holds the prompt's text, and only the host that
	// admitted the prompt knows the rest.
	RecordPromptAttachments(ctx context.Context, sessionID, runID string, attachments []PromptAttachment) error
	// PromptAttachments returns what one run's prompt carried, which is the read
	// half of the same record: the console's page and history projections are
	// served from the log, so they read it back by run id.
	PromptAttachments(ctx context.Context, runID string) ([]PromptAttachment, error)
	// MaterializePromptFile publishes a stored file as a verbatim read-only copy
	// the agent's own file tools can read, and returns the path the run is told to
	// read -- relative to the tool world when the store has one, which is what a
	// workspace-rooted file tool accepts. An empty path means the store could not
	// publish one, and the model is told that instead.
	MaterializePromptFile(ctx context.Context, sessionID, attachmentID string) (string, error)
}

// PromptAttachment is one attachment a prompt carried, as the transcript shows
// it. An image names its stored attachment, media type and pixel dimensions; a
// file names its stored attachment and size, and the model-visible copy the run
// was told to read.
type PromptAttachment struct {
	Kind         string `json:"kind"`
	AttachmentID string `json:"attachmentId"`
	Name         string `json:"name,omitempty"`
	MediaType    string `json:"mediaType,omitempty"`
	Bytes        int    `json:"bytes"`
	Width        int    `json:"width,omitempty"`
	Height       int    `json:"height,omitempty"`
	// Path is where a file's verbatim read-only copy was published, when the
	// store could publish one. It is what the run was told to read.
	Path string `json:"path,omitempty"`
}

// ErrAttachmentNotFound reports an id that this session has no stored attachment
// for. The store reports it so the handler can answer the console's own
// `session/attachment-invalid` code instead of a generic internal failure.
var ErrAttachmentNotFound = errors.New("attachment is not stored for this session")

// attachmentMaxBytes bounds one uploaded attachment. It is the store's own
// ceiling, well above one image (model.MaxImageBytes) because a non-image file
// has no reason to be small; the unary route is additionally bounded by the
// console envelope limit (maxRequestBodyBytes), and the raw route by this value.
const attachmentMaxBytes = 64 << 20

// attachmentInvalidCode is the console's code for an attachment it cannot use.
// The pinned client declares exactly one attachment code, `session/attachment-invalid`
// (`@deepseek-ai/dsh-client-file-upload` and the session controller's
// `session/attachment-invalid` for a receipt that was never staged), so every
// refusal in this family answers it and names the specific reason in `details`.
const attachmentInvalidCode = "session/attachment-invalid"

// SetAttachments installs the attachment store.
func (h *Handler) SetAttachments(store AttachmentStore) {
	h.attachmentsMu.Lock()
	h.attachments = store
	h.attachmentsMu.Unlock()
}

func (h *Handler) attachmentsSource() AttachmentStore {
	h.attachmentsMu.RLock()
	defer h.attachmentsMu.RUnlock()
	return h.attachments
}

// attachmentsUnavailable refuses the family when no store is mounted. It names
// the dependency, and it keeps saying so with the sentence the two halves have
// always used, because a host with no store still has no attachment to read.
func (h *Handler) attachmentsUnavailable() *methodError {
	return fail(codeUnimplemented, attachmentRefusal,
		map[string]any{"capability": "an attachment store"})
}

// fileUploadsUpload answers POST /api/fileUploads/upload: the console's upload
// route for bytes it holds in memory. Its scope is the reference's
// `{context: "agent", wire: "agentId"}` and its argument is `request`:
// `{data: <base64>, name?: <string>}`, the canonical base64 form the client
// builds with `bytesToBase64`.
func (h *Handler) fileUploadsUpload(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	// The dispatcher flattens the parameter object into the argument map, so the
	// names checked here are the request's own: `data` and `name`, next to the
	// scope's `agentId` (requestargs.go).
	if failure := rejectUnknownArguments(args, "agentId", "data", "name"); failure != nil {
		return nil, failure
	}
	sessionID, failure := h.fileReferenceSession(ctx, args)
	if failure != nil {
		return nil, failure
	}
	encoded, present, failure := stringArg(args, "data")
	if failure != nil {
		return nil, failure
	}
	if !present {
		return nil, argumentRequired("request.data")
	}
	name := ""
	if value, present, failure := stringArg(args, "name"); failure != nil {
		return nil, failure
	} else if present {
		name = value
	}
	data, decodeErr := base64.StdEncoding.DecodeString(encoded)
	if decodeErr != nil {
		return nil, fail(codeArgumentsInvalid, `"request.data" must be canonical base64`,
			map[string]any{"argument": "request.data"})
	}
	descriptor, methodFailure := h.storeAttachment(ctx, sessionID, name, data)
	if methodFailure != nil {
		return nil, methodFailure
	}
	return uploadReceipt(descriptor), nil
}

// serveRawFileUpload answers the console's raw-byte upload route,
// `/api/session/uploadFileBinary`. It is not part of the unary envelope: the
// request body is the file itself (`application/octet-stream`), the session and
// the display name are query parameters, and the answer is the same `{ok, value}`
// object `parseFileUploadResult` reads from the RPC response.
//
// It is dispatched before the envelope is parsed, because the body is not JSON
// and there is no `rpcId` to answer with; everything else about it -- the trust
// fence, the session check, the store path -- is the route above.
func (h *Handler) serveRawFileUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeRawUploadFailure(w, http.StatusMethodNotAllowed, "session/attachment-invalid",
			"a file upload must be a POST with the bytes as its body")
		return
	}
	query := r.URL.Query()
	sessionID := strings.TrimSpace(query.Get("sessionId"))
	if sessionID == "" {
		writeRawUploadFailure(w, http.StatusBadRequest, attachmentInvalidCode,
			`the upload route requires a "sessionId" query parameter`)
		return
	}
	if !h.sessionKnown(r.Context(), sessionID) {
		writeRawUploadFailure(w, http.StatusNotFound, codeSessionNotFound,
			fmt.Sprintf("unknown session %q", sessionID))
		return
	}
	data, err := readBoundedBody(r, attachmentMaxBytes)
	if err != nil {
		writeRawUploadFailure(w, http.StatusRequestEntityTooLarge, attachmentInvalidCode,
			fmt.Sprintf("the uploaded bytes exceed this host's %d-byte attachment limit", attachmentMaxBytes))
		return
	}
	descriptor, failure := h.storeAttachment(r.Context(), sessionID, strings.TrimSpace(query.Get("name")), data)
	if failure != nil {
		writeRawUploadFailure(w, http.StatusBadRequest, failure.code, failure.message)
		return
	}
	writeAttachmentJSON(w, http.StatusOK, map[string]any{"ok": true, "value": uploadReceipt(descriptor)})
}

// storeAttachment is the one path both upload routes share, so a file stored
// through either is stored the same way.
func (h *Handler) storeAttachment(ctx context.Context, sessionID, name string, data []byte) (AttachmentDescriptor, *methodError) {
	store := h.attachmentsSource()
	if store == nil {
		return AttachmentDescriptor{}, h.attachmentsUnavailable()
	}
	if len(data) == 0 {
		return AttachmentDescriptor{}, fail(attachmentInvalidCode, "an uploaded file carries no bytes",
			map[string]any{"reason": "EMPTY_UPLOAD"})
	}
	if len(data) > attachmentMaxBytes {
		return AttachmentDescriptor{}, fail(attachmentInvalidCode,
			fmt.Sprintf("the uploaded bytes exceed this host's %d-byte attachment limit", attachmentMaxBytes),
			map[string]any{"reason": "TOO_LARGE", "limit": attachmentMaxBytes, "bytes": len(data)})
	}
	descriptor, err := store.Save(ctx, sessionID, name, data)
	if err != nil {
		return AttachmentDescriptor{}, fail(codeInternal, fmt.Sprintf("store attachment: %v", err), nil)
	}
	if descriptor.ID == "" {
		return AttachmentDescriptor{}, fail(codeInternal, "the attachment store returned no id", nil)
	}
	return descriptor, nil
}

// uploadReceipt is the shape both upload routes answer with: the id the console
// cites in a later prompt and the file's name and size, exactly the three fields
// `parseFileUploadResult` validates.
func uploadReceipt(descriptor AttachmentDescriptor) map[string]any {
	return map[string]any{
		"receiptId": descriptor.ID,
		"file": map[string]any{
			"attachmentId": descriptor.ID,
			"name":         descriptor.Name,
			"bytes":        descriptor.Bytes,
		},
	}
}

// sessionAttachment answers POST /api/session/attachment: the read half, and the
// method the console calls to render an attachment a message refers to.
func (h *Handler) sessionAttachment(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	if failure := rejectUnknownArguments(args, "sessionId", "attachmentId"); failure != nil {
		return nil, failure
	}
	sessionID, present, failure := stringArg(args, "sessionId")
	if failure != nil {
		return nil, failure
	}
	if !present || strings.TrimSpace(sessionID) == "" {
		return nil, argumentRequired("sessionId")
	}
	attachmentID, present, failure := stringArg(args, "attachmentId")
	if failure != nil {
		return nil, failure
	}
	if !present || strings.TrimSpace(attachmentID) == "" {
		return nil, argumentRequired("attachmentId")
	}
	store := h.attachmentsSource()
	if store == nil {
		return nil, h.attachmentsUnavailable()
	}
	descriptor, data, err := store.Read(ctx, sessionID, attachmentID)
	if err != nil {
		if errors.Is(err, ErrAttachmentNotFound) {
			return nil, fail(attachmentInvalidCode,
				fmt.Sprintf("attachment %q is not stored for this session", attachmentID),
				map[string]any{"reason": "ATTACHMENT_NOT_FOUND", "attachmentId": attachmentID})
		}
		return nil, fail(codeInternal, fmt.Sprintf("read attachment: %v", err), nil)
	}
	if !model.SupportedImageMediaType(descriptor.MediaType) {
		return nil, fail(attachmentInvalidCode,
			"this host stores the attachment and can hand its bytes back, but its read result is an image descriptor and these bytes are not an image it can describe",
			map[string]any{"reason": "NOT_AN_IMAGE", "attachmentId": attachmentID, "mediaType": descriptor.MediaType})
	}
	if descriptor.Width <= 0 || descriptor.Height <= 0 {
		return nil, fail(attachmentInvalidCode,
			fmt.Sprintf("this host stored the %s image but cannot read its dimensions, so it cannot describe it", descriptor.MediaType),
			map[string]any{"reason": "DIMENSIONS_UNREADABLE", "attachmentId": attachmentID, "mediaType": descriptor.MediaType})
	}
	attachment := map[string]any{
		"attachmentId": descriptor.ID,
		"mediaType":    descriptor.MediaType,
		"bytes":        descriptor.Bytes,
		"width":        descriptor.Width,
		"height":       descriptor.Height,
	}
	if descriptor.Name != "" {
		attachment["name"] = descriptor.Name
	}
	return map[string]any{
		"attachment": attachment,
		"data":       base64.StdEncoding.EncodeToString(data),
	}, nil
}

// decodePromptContentImages turns the prompt's `image` content parts into model
// images. The console sends them inline rather than uploading them first
// (`ui-conversation` `encodeImage`): `{type: "image", mediaType, data, name?}`
// with the canonical base64 the browser read from the picked file.
//
// The media type is checked against the bytes, not trusted: a mislabelled part
// would otherwise be sent to a provider as a format it is not, and the size is
// bounded by the same limit the model path enforces, because the image is
// replayed on every later request of the conversation.
func decodePromptContentImages(parts []json.RawMessage) ([]promptImagePart, *methodError) {
	images := make([]promptImagePart, 0, len(parts))
	for _, rawPart := range parts {
		part, err := decodeJSONObject(rawPart)
		if err != nil {
			continue
		}
		if partType, _ := rawJSONString(part["type"]); partType != "image" {
			continue
		}
		mediaType, _, failure := stringArg(part, "mediaType")
		if failure != nil {
			return nil, failure
		}
		if mediaType == "" {
			return nil, fail(codeArgumentsInvalid, `an "image" content part requires "mediaType"`,
				map[string]any{"argument": "content"})
		}
		encoded, present, failure := stringArg(part, "data")
		if failure != nil {
			return nil, failure
		}
		if !present || encoded == "" {
			return nil, fail(codeArgumentsInvalid, `an "image" content part requires base64 "data"`,
				map[string]any{"argument": "content"})
		}
		data, decodeErr := base64.StdEncoding.DecodeString(encoded)
		if decodeErr != nil {
			return nil, fail(codeArgumentsInvalid, `an "image" content part's "data" must be canonical base64`,
				map[string]any{"argument": "content"})
		}
		detected := model.DetectImageMediaType(data)
		if detected == "" {
			return nil, fail(codeUnsupportedContent,
				"an image content part must carry PNG, JPEG, GIF or WebP bytes that match its media type",
				map[string]any{"part": "image", "mediaType": mediaType})
		}
		if detected != mediaType {
			return nil, fail(codeUnsupportedContent,
				fmt.Sprintf("the image content part declares %q but its bytes are %q", mediaType, detected),
				map[string]any{"part": "image", "mediaType": mediaType, "detected": detected})
		}
		if len(data) > model.MaxImageBytes {
			return nil, fail(attachmentInvalidCode,
				fmt.Sprintf("the image exceeds this host's %d-byte limit per image", model.MaxImageBytes),
				map[string]any{"reason": "IMAGES_TOO_LARGE", "bytes": len(data), "limit": model.MaxImageBytes})
		}
		name, _, failure := stringArg(part, "name")
		if failure != nil {
			return nil, failure
		}
		images = append(images, promptImagePart{name: name, data: data})
	}
	if len(images) == 0 {
		return nil, nil
	}
	// One message's images are replayed together on every later request, so the
	// aggregate is bounded too -- the same reason the per-image limit exists.
	total := 0
	for _, image := range images {
		total += len(image.data)
	}
	if total > model.MaxImageBytes {
		return nil, fail(attachmentInvalidCode,
			fmt.Sprintf("the prompt's images exceed this host's %d-byte limit per message", model.MaxImageBytes),
			map[string]any{"reason": "IMAGES_TOO_LARGE", "bytes": total, "limit": model.MaxImageBytes})
	}
	return images, nil
}

// decodeFilePromptPart reads a `file` content part into the receipt it cites. The
// console uploads a file first and then cites its receipt
// (`serializeDraftAttachments`: `{type: "file", receiptId}`), so this is the id
// the store is asked to resolve -- and an id it never staged is the console's own
// attachment error, reported by admission rather than here.
func decodeFilePromptPart(part map[string]json.RawMessage) (promptFilePart, *methodError) {
	receiptID, _, failure := stringArg(part, "receiptId")
	if failure != nil {
		return promptFilePart{}, failure
	}
	if receiptID == "" {
		return promptFilePart{}, fail(codeArgumentsInvalid, `a "file" content part requires "receiptId"`,
			map[string]any{"argument": "content"})
	}
	name, _, failure := stringArg(part, "name")
	if failure != nil {
		return promptFilePart{}, failure
	}
	return promptFilePart{receiptID: receiptID, name: name}, nil
}

// attachmentRefusal is the one sentence the family refuses with when no store is
// mounted, so a host without one cannot tell two different stories about why.
const attachmentRefusal = "this host has no attachment store: a prompt's image and file parts are refused when they are submitted, so there is no attachment to read; put the file in the workspace and ask the agent to read it"

// errBodyTooLarge reports a raw upload over the store's ceiling. It is distinct
// from a read failure so the route can answer "too large" rather than "broken".
var errBodyTooLarge = errors.New("request body exceeds the limit")

// readBoundedBody reads a raw request body up to limit bytes. It reads one byte
// past the limit so an oversized body is detected without buffering all of it.
func readBoundedBody(r *http.Request, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errBodyTooLarge
	}
	return data, nil
}

// attachmentObjectArg reads a required object argument.
func attachmentObjectArg(args map[string]json.RawMessage, name string) (map[string]json.RawMessage, *methodError) {
	raw, ok := args[name]
	if !ok {
		return nil, argumentRequired(name)
	}
	object, err := decodeJSONObject(raw)
	if err != nil {
		return nil, fail(codeArgumentsInvalid, fmt.Sprintf("%q must be a JSON object", name),
			map[string]any{"argument": name})
	}
	return object, nil
}

// writeAttachmentJSON writes one raw-route answer. The console parses this body
// as JSON with no envelope, so the content type is set here rather than left to
// sniffing.
func writeAttachmentJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeRawUploadFailure writes the failure half of the raw route's answer in the
// shape `parseFileUploadResult` reads: `{ok: false, error: {code, message,
// details}}`. details is always an object because the client validates it.
func writeRawUploadFailure(w http.ResponseWriter, status int, code, message string) {
	writeAttachmentJSON(w, status, map[string]any{
		"ok": false,
		"error": map[string]any{
			"code":    code,
			"message": message,
			"details": map[string]any{},
		},
	})
}

// jsonString reads a JSON string that has already been decoded, reporting
// whether it was one.
func rawJSONString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}
