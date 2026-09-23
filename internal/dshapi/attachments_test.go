package dshapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/internal/dshwire"
	"github.com/feiyu912/zenforge/model"
)

// The attachment family is pinned in three directions: the vendored shapes it
// answers with (the upload receipt the client validates field by field, the read
// result's two keys), the behavior of a host with a store (both upload routes
// land in it, the read method describes a stored image and refuses what it cannot
// describe), and the honest answer of a host without one.
func TestAttachmentEnvelopesMatchTheVendoredConsole(t *testing.T) {
	console := sessionBundle(t)
	uploads := vendoredBundle{
		source: readSource(t, sessionRemotePath),
		pkg:    "@deepseek-ai/dsh-client-file-upload",
		ns:     "fileUploads",
	}

	upload := uploads.descriptor(t, "upload")
	if wires := uploads.wireNames(t, upload, "upload"); !sameStrings(wires, []string{"agentId", "request"}) {
		t.Fatalf("fileUploads/upload wires = %v, want the agent scope and the request", wires)
	}
	objects := uploads.objectParameters(t, "upload")
	if len(objects) != 1 {
		t.Fatalf("fileUploads/upload object parameters = %v, want one", objects)
	}
	if keys := sortedKeys(objects[0]); !sameStrings(keys, []string{"data", "name"}) {
		t.Fatalf("fileUploads/upload request keys = %v, want the base64 data and the optional name", keys)
	}
	result := uploads.schemaExpression(t, "upload", "result")
	if keys := sortedKeys(topLevelKeys(t, result)); !sameStrings(keys, []string{"file", "receiptId"}) {
		t.Fatalf("fileUploads/upload result keys = %v, want the receipt and the file", keys)
	}
	for _, field := range []string{`"attachmentId"`, `"name"`, `"bytes"`} {
		if !strings.Contains(result, field) {
			t.Fatalf("fileUploads/upload result = %q, want the file's %s", result, field)
		}
	}

	read := console.descriptor(t, "attachment")
	if wires := console.wireNames(t, read, "attachment"); !sameStrings(wires, []string{"request"}) {
		t.Fatalf("session/attachment wires = %v, want the request object", wires)
	}
	objects = console.objectParameters(t, "attachment")
	if len(objects) != 1 {
		t.Fatalf("session/attachment object parameters = %v, want one", objects)
	}
	if keys := sortedKeys(objects[0]); !sameStrings(keys, []string{"attachmentId", "sessionId"}) {
		t.Fatalf("session/attachment request keys = %v, want the session and attachment ids", keys)
	}
	// The read half answers the metadata and the bytes, and its descriptor is an
	// image descriptor: the media type is the four-value image union and the
	// dimensions are required, which is why a non-image is refused rather than
	// described.
	result = console.schemaExpression(t, "attachment", "result")
	if keys := sortedKeys(topLevelKeys(t, result)); !sameStrings(keys, []string{"attachment", "data"}) {
		t.Fatalf("session/attachment result keys = %v, want the attachment and its data", keys)
	}
	if !strings.Contains(result, `"mediaType"`) || !strings.Contains(result, "image/png") {
		t.Fatalf("session/attachment result = %q, want the image metadata the console reads", result)
	}
	// The dispatcher flattens the parameter object, so the names each handler
	// accepts are the request's own fields next to the scope's agent id.
	for _, argument := range []struct {
		function string
		want     []string
	}{
		{"fileUploadsUpload", []string{"agentId", "data", "name"}},
		{"sessionAttachment", []string{"attachmentId", "sessionId"}},
	} {
		if accepted := handlerArgumentNames(t, readSource(t, "attachments.go"), argument.function); !sameStrings(accepted, argument.want) {
			t.Fatalf("%s accepts %v, want %v", argument.function, accepted, argument.want)
		}
	}
}

// A host with no store answers the whole family the same way: unimplemented,
// naming the store, with the sentence the family has always used -- and the raw
// route answers its own `{ok: false, error}` shape rather than a unary envelope.
func TestAttachmentsWithoutAStoreNameTheMissingCapability(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.createSession(t)
	for _, call := range []struct {
		path   string
		method string
		args   string
	}{
		{"/api/fileUploads/upload", "fileUploads/upload",
			`{"agentId":` + mustJSON(t, sessionID) + `,"request":{"data":"aGk="}}`},
		{"/api/session/attachment", "session/attachment",
			`{"sessionId":` + mustJSON(t, sessionID) + `,"attachmentId":"att-1"}`},
	} {
		envelope := decodeResponse(t, f.post(t, call.path, rpcBody(t, "s1", call.method, call.args)))
		if envelope.Result.OK {
			t.Fatalf("%s: result = %s, want a refusal", call.method, envelope.Result.Value)
		}
		if envelope.Result.Error.Code != codeUnimplemented {
			t.Fatalf("%s: code = %q, want %q", call.method, envelope.Result.Error.Code, codeUnimplemented)
		}
		if capability, _ := envelope.Result.Error.Details["capability"].(string); capability != "an attachment store" {
			t.Fatalf("%s: details = %+v, want the missing store named", call.method, envelope.Result.Error.Details)
		}
		if !strings.Contains(envelope.Result.Error.Message, "workspace") {
			t.Fatalf("%s: message = %q, want the substitute named", call.method, envelope.Result.Error.Message)
		}
	}

	rawBody := f.postRawUpload(t, sessionID, "note.txt", []byte("hello"))
	body := decodeRawUpload(t, rawBody)
	if ok, _ := body["ok"].(bool); ok {
		t.Fatalf("raw upload = %s, want a refusal without a store", rawBody.String())
	}
	errorBody, _ := body["error"].(map[string]any)
	if errorBody["code"] != codeUnimplemented {
		t.Fatalf("raw upload code = %v, want %q", errorBody["code"], codeUnimplemented)
	}
	if _, ok := errorBody["details"].(map[string]any); !ok {
		t.Fatalf("raw upload details = %v, want an object (the client validates it)", errorBody["details"])
	}
}

// The unary route stores the bytes it was handed and answers the receipt the
// client validates: the id, and the file's name and size.
func TestUploadStoresBytesAndAnswersAReceipt(t *testing.T) {
	store := newMemoryAttachments()
	f := newFixture(t, Config{})
	f.handler.SetAttachments(store)
	sessionID := f.createSession(t)

	payload := []byte("hello attachment")
	envelope := decodeResponse(t, f.post(t, "/api/fileUploads/upload", rpcBody(t, "s1", "fileUploads/upload",
		`{"agentId":`+mustJSON(t, sessionID)+`,"request":{"data":`+mustJSON(t, base64.StdEncoding.EncodeToString(payload))+`,"name":"note.txt"}}`)))
	if !envelope.Result.OK {
		t.Fatalf("upload = %s, want a receipt", envelope.Result.Error)
	}
	var receipt struct {
		ReceiptID string `json:"receiptId"`
		File      struct {
			AttachmentID string `json:"attachmentId"`
			Name         string `json:"name"`
			Bytes        int    `json:"bytes"`
		} `json:"file"`
	}
	decodeJSONInto(t, envelope.Result.Value, &receipt)
	if receipt.ReceiptID == "" || receipt.ReceiptID != receipt.File.AttachmentID {
		t.Fatalf("receipt = %+v, want one id used for both fields", receipt)
	}
	if receipt.File.Name != "note.txt" || receipt.File.Bytes != len(payload) {
		t.Fatalf("file = %+v, want the name and size that were uploaded", receipt.File)
	}
	store.mustHave(t, sessionID, receipt.ReceiptID, payload)

	// A request that is not canonical base64 is an argument error, not a stored
	// file: the console builds this field with bytesToBase64, so anything else is
	// a caller bug worth naming.
	envelope = decodeResponse(t, f.post(t, "/api/fileUploads/upload", rpcBody(t, "s2", "fileUploads/upload",
		`{"agentId":`+mustJSON(t, sessionID)+`,"request":{"data":"not base64!!"}}`)))
	if envelope.Result.OK || envelope.Result.Error.Code != codeArgumentsInvalid {
		t.Fatalf("bad base64 = %+v, want an arguments-invalid refusal", envelope.Result)
	}
}

// The raw route is the console's other upload entry point: bytes in the body,
// the session and name in the query, and the same `{ok, value}` object.
func TestRawUploadRouteStoresTheBodyBytes(t *testing.T) {
	store := newMemoryAttachments()
	f := newFixture(t, Config{})
	f.handler.SetAttachments(store)
	sessionID := f.createSession(t)

	payload := []byte("raw bytes")
	body := decodeRawUpload(t, f.postRawUpload(t, sessionID, "photo.bin", payload))
	if ok, _ := body["ok"].(bool); !ok {
		t.Fatalf("raw upload = %v, want ok", body)
	}
	value, _ := body["value"].(map[string]any)
	receiptID, _ := value["receiptId"].(string)
	if receiptID == "" {
		t.Fatalf("raw upload = %v, want a receiptId", body)
	}
	store.mustHave(t, sessionID, receiptID, payload)

	// The route is scoped: an unknown session is refused before any bytes are
	// stored, and the method is POST.
	body = decodeRawUpload(t, f.postRawUpload(t, "session-does-not-exist", "x.bin", []byte("x")))
	errorBody, _ := body["error"].(map[string]any)
	if errorBody["code"] != codeSessionNotFound {
		t.Fatalf("unknown session code = %v, want %q", errorBody["code"], codeSessionNotFound)
	}
	if len(store.saved()) != 1 {
		t.Fatalf("store holds %d uploads, want only the accepted one", len(store.saved()))
	}
}

// The read half describes a stored image with the dimensions it can read, serves
// its bytes, and refuses by name everything its image descriptor cannot honestly
// describe.
func TestSessionAttachmentServesAStoredImage(t *testing.T) {
	store := newMemoryAttachments()
	f := newFixture(t, Config{})
	f.handler.SetAttachments(store)
	sessionID := f.createSession(t)

	encoded := encodePNG(t, 3, 2)
	descriptor := store.mustSave(t, sessionID, "pixels.png", encoded)
	if descriptor.Width != 3 || descriptor.Height != 2 {
		t.Fatalf("stored descriptor = %+v, want the image's real dimensions", descriptor)
	}

	envelope := decodeResponse(t, f.post(t, "/api/session/attachment", rpcBody(t, "s1", "session/attachment",
		`{"sessionId":`+mustJSON(t, sessionID)+`,"attachmentId":`+mustJSON(t, descriptor.ID)+`}`)))
	if !envelope.Result.OK {
		t.Fatalf("read = %s, want the attachment", envelope.Result.Error)
	}
	var read struct {
		Attachment struct {
			AttachmentID string `json:"attachmentId"`
			MediaType    string `json:"mediaType"`
			Bytes        int    `json:"bytes"`
			Width        int    `json:"width"`
			Height       int    `json:"height"`
			Name         string `json:"name"`
		} `json:"attachment"`
		Data string `json:"data"`
	}
	decodeJSONInto(t, envelope.Result.Value, &read)
	if read.Attachment.MediaType != "image/png" || read.Attachment.Width != 3 || read.Attachment.Height != 2 {
		t.Fatalf("attachment = %+v, want the image descriptor the console requires", read.Attachment)
	}
	if read.Attachment.Name != "pixels.png" || read.Attachment.Bytes != len(encoded) {
		t.Fatalf("attachment = %+v, want the stored name and size", read.Attachment)
	}
	data, err := base64.StdEncoding.DecodeString(read.Data)
	if err != nil || !bytes.Equal(data, encoded) {
		t.Fatalf("data = %q, want the exact stored bytes", read.Data)
	}

	// An id this session never stored is not-found, and it is not answered with
	// another session's attachment: the store scopes reads by session.
	envelope = decodeResponse(t, f.post(t, "/api/session/attachment", rpcBody(t, "s2", "session/attachment",
		`{"sessionId":`+mustJSON(t, sessionID)+`,"attachmentId":"att-00000000000000000000000000000000"}`)))
	if envelope.Result.OK || envelope.Result.Error.Code != attachmentInvalidCode {
		t.Fatalf("unknown id = %+v, want %q", envelope.Result, attachmentInvalidCode)
	}
	if reason, _ := envelope.Result.Error.Details["reason"].(string); reason != "ATTACHMENT_NOT_FOUND" {
		t.Fatalf("unknown id details = %+v, want the reason named", envelope.Result.Error.Details)
	}

	// A file that is not an image cannot be described by the image descriptor the
	// console's contract declares, and the refusal says which half is missing.
	text := store.mustSave(t, sessionID, "note.txt", []byte("just text"))
	envelope = decodeResponse(t, f.post(t, "/api/session/attachment", rpcBody(t, "s3", "session/attachment",
		`{"sessionId":`+mustJSON(t, sessionID)+`,"attachmentId":`+mustJSON(t, text.ID)+`}`)))
	if envelope.Result.OK || envelope.Result.Error.Code != attachmentInvalidCode {
		t.Fatalf("non-image = %+v, want %q", envelope.Result, attachmentInvalidCode)
	}
	if reason, _ := envelope.Result.Error.Details["reason"].(string); reason != "NOT_AN_IMAGE" {
		t.Fatalf("non-image details = %+v, want NOT_AN_IMAGE", envelope.Result.Error.Details)
	}

	// A real WebP is measured and served like any other image now that the header
	// reader exists (ADR 0139).
	webp := store.mustSave(t, sessionID, "picture.webp", decodeFixture(t, testWebPBase64))
	if webp.MediaType != "image/webp" || webp.Width != 4 || webp.Height != 3 {
		t.Fatalf("webp descriptor = %+v, want a measured WebP", webp)
	}
	envelope = decodeResponse(t, f.post(t, "/api/session/attachment", rpcBody(t, "s4", "session/attachment",
		`{"sessionId":`+mustJSON(t, sessionID)+`,"attachmentId":`+mustJSON(t, webp.ID)+`}`)))
	if !envelope.Result.OK {
		t.Fatalf("webp read = %s, want the attachment", envelope.Result.Error)
	}

	// A WebP whose header is truncated is still stored, with no dimensions, and
	// the read names the missing measurement instead of guessing a size.
	broken := store.mustSave(t, sessionID, "broken.webp", decodeFixture(t, testWebPBase64)[:20])
	if broken.MediaType != "image/webp" || broken.Width != 0 {
		t.Fatalf("broken descriptor = %+v, want the type with no dimensions", broken)
	}
	envelope = decodeResponse(t, f.post(t, "/api/session/attachment", rpcBody(t, "s5", "session/attachment",
		`{"sessionId":`+mustJSON(t, sessionID)+`,"attachmentId":`+mustJSON(t, broken.ID)+`}`)))
	if envelope.Result.OK || envelope.Result.Error.Code != attachmentInvalidCode {
		t.Fatalf("broken webp = %+v, want %q", envelope.Result, attachmentInvalidCode)
	}
	if reason, _ := envelope.Result.Error.Details["reason"].(string); reason != "DIMENSIONS_UNREADABLE" {
		t.Fatalf("broken webp details = %+v, want DIMENSIONS_UNREADABLE", envelope.Result.Error.Details)
	}
	if !strings.Contains(envelope.Result.Error.Message, "image/webp") {
		t.Fatalf("broken webp message = %q, want the format named", envelope.Result.Error.Message)
	}
}

// testWebPBase64 is a real 4x3 lossless WebP produced by Pillow, embedded so the
// family's own test measures a real container.
const testWebPBase64 = "UklGRh4AAABXRUJQVlA4TBEAAAAvA4AAAAdQiirUo/+BiOh/AAA="

func decodeFixture(t *testing.T, encoded string) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return data
}

// An image content part travels inline in the prompt and is delivered to the run
// the prompt starts; a file part cites a receipt, is resolved from the store, and
// is delivered as the model-visible handle text the reference host uses.
func TestPromptAdmitsImagesAndDeliversFilesAsHandles(t *testing.T) {
	f := newFixture(t, Config{})
	f.handler.SetAttachments(newMemoryAttachments())
	sessionID := f.createSession(t)

	encoded := encodePNG(t, 4, 5)
	envelope := decodeResponse(t, f.post(t, "/api/session/prompt", rpcBody(t, "rpc-prompt", "session/prompt",
		`{"requestId":"req-1","sessionId":`+mustJSON(t, sessionID)+
			`,"mode":"queue","content":[{"type":"text","text":"what is this"},`+
			`{"type":"image","mediaType":"image/png","data":`+mustJSON(t, base64.StdEncoding.EncodeToString(encoded))+`,"name":"pixels.png"}]}`)))
	if !envelope.Result.OK {
		t.Fatalf("prompt = %s, want it accepted", envelope.Result.Error)
	}
	task := f.lastTask(t)
	if len(task.Images) != 1 {
		t.Fatalf("task images = %+v, want the one image the prompt carried", task.Images)
	}
	if task.Images[0].MediaType != "image/png" || !bytes.Equal(task.Images[0].Data, encoded) {
		t.Fatalf("task image = %+v, want the bytes the prompt carried", task.Images[0])
	}

	// The type is checked against the bytes: a part that declares PNG and carries
	// text would otherwise be sent to a provider as an image it is not.
	for _, content := range []string{
		`{"type":"image","mediaType":"image/png","data":` + mustJSON(t, base64.StdEncoding.EncodeToString([]byte("not an image"))) + `}`,
		`{"type":"image","mediaType":"image/jpeg","data":` + mustJSON(t, base64.StdEncoding.EncodeToString(encoded)) + `}`,
	} {
		envelope := decodeResponse(t, f.post(t, "/api/session/prompt", rpcBody(t, "rpc-x", "session/prompt",
			`{"requestId":"req-2","sessionId":`+mustJSON(t, sessionID)+`,"mode":"queue","content":[{"type":"text","text":"hello"},`+content+`]}`)))
		if envelope.Result.OK {
			t.Fatalf("%s: prompt = %s, want a refusal", content, envelope.Result.Value)
		}
		if envelope.Result.Error.Code != codeUnsupportedContent {
			t.Fatalf("%s: code = %q, want %q", content, envelope.Result.Error.Code, codeUnsupportedContent)
		}
	}

	// A file that was staged is delivered: the run's input carries the reference
	// host's handle text naming the file, its size, its digest prefix and the
	// read-only copy the agent can read, and the transcript block names the stored
	// attachment.
	fileSession := f.createSession(t)
	staged, failure := f.handler.storeAttachment(context.Background(), fileSession, "notes.txt", []byte("the file's words"))
	if failure != nil {
		t.Fatalf("stage file: %v", failure)
	}
	envelope = decodeResponse(t, f.post(t, "/api/session/prompt", rpcBody(t, "rpc-file", "session/prompt",
		`{"requestId":"req-3","sessionId":`+mustJSON(t, fileSession)+`,"mode":"queue","content":[{"type":"text","text":"read this"},{"type":"file","receiptId":`+mustJSON(t, staged.ID)+`}]}`)))
	if !envelope.Result.OK {
		t.Fatalf("file prompt = %s, want it accepted", envelope.Result.Error)
	}
	task = f.lastTask(t)
	if !strings.Contains(task.Input, `File "notes.txt" (16 bytes, sha256:`+strings.TrimPrefix(staged.ID, "att-")[:8]+`)`) ||
		!strings.Contains(task.Input, "verbatim read-only copy saved at") {
		t.Fatalf("task input = %q, want the handle text naming the file", task.Input)
	}
	if len(task.Images) != 0 {
		t.Fatalf("task images = %+v, want a file to add none", task.Images)
	}
	if strings.Index(task.Input, "File ") > strings.Index(task.Input, "read this") {
		t.Fatalf("task input = %q, want the handle before the words", task.Input)
	}
	// The operator's own words travel apart from the model's input, so the
	// conversation is not titled with a file handle.
	if got, _ := task.Meta[zenforge.MetaPromptText].(string); got != "read this" {
		t.Fatalf("task meta prompt text = %q, want the operator's words", got)
	}

	// A receipt this session never staged is the console's own attachment error,
	// named, rather than a sentence about a carrier.
	unknownSession := f.createSession(t)
	envelope = decodeResponse(t, f.post(t, "/api/session/prompt", rpcBody(t, "rpc-file-unknown", "session/prompt",
		`{"requestId":"req-4","sessionId":`+mustJSON(t, unknownSession)+`,"mode":"queue","content":[{"type":"text","text":"hello"},{"type":"file","receiptId":"att-deadbeef"}]}`)))
	if envelope.Result.OK || envelope.Result.Error.Code != attachmentInvalidCode {
		t.Fatalf("unknown receipt = %+v, want %q", envelope.Result, attachmentInvalidCode)
	}
	if reason, _ := envelope.Result.Error.Details["reason"].(string); reason != "ATTACHMENT_NOT_FOUND" {
		t.Fatalf("unknown receipt details = %+v, want ATTACHMENT_NOT_FOUND", envelope.Result.Error.Details)
	}

	// A file part with no receipt at all is a malformed part, not a refusal.
	envelope = decodeResponse(t, f.post(t, "/api/session/prompt", rpcBody(t, "rpc-file-none", "session/prompt",
		`{"requestId":"req-5","sessionId":`+mustJSON(t, unknownSession)+`,"mode":"queue","content":[{"type":"text","text":"hello"},{"type":"file"}]}`)))
	if envelope.Result.OK || envelope.Result.Error.Code != codeArgumentsInvalid {
		t.Fatalf("receiptless file = %+v, want %q", envelope.Result, codeArgumentsInvalid)
	}
}

// The run queue and the steer path carry the next turn's text, so an image
// cannot follow them. It is refused by name rather than dropped.
func TestPromptRefusesAnImageIntoALiveRun(t *testing.T) {
	f := newFixture(t, Config{})
	f.handler.SetAttachments(newMemoryAttachments())
	sessionID := f.startSession(t)

	encoded := encodePNG(t, 2, 2)
	envelope := decodeResponse(t, f.post(t, "/api/session/prompt", rpcBody(t, "rpc-live", "session/prompt",
		`{"requestId":"req-live","sessionId":`+mustJSON(t, sessionID)+
			`,"mode":"queue","content":[{"type":"text","text":"look"},`+
			`{"type":"image","mediaType":"image/png","data":`+mustJSON(t, base64.StdEncoding.EncodeToString(encoded))+
			`,"name":"queued.png"}]}`)))
	if envelope.Result.OK {
		t.Fatalf("prompt = %s, want the image refused into a live run", envelope.Result.Value)
	}
	if envelope.Result.Error.Code != codeUnsupportedContent {
		t.Fatalf("code = %q, want %q", envelope.Result.Error.Code, codeUnsupportedContent)
	}
	if !strings.Contains(envelope.Result.Error.Message, "already answering") {
		t.Fatalf("message = %q, want the live run named", envelope.Result.Error.Message)
	}
	if mode, _ := envelope.Result.Error.Details["mode"].(string); mode != "queue" {
		t.Fatalf("details = %+v, want the mode the console sent", envelope.Result.Error.Details)
	}
}

// memoryAttachments is a store that keeps bytes in memory and derives the
// descriptor the way a real store does: the media type from the bytes and the
// dimensions from the image header, when this host can read it.
type memoryAttachments struct {
	prompts map[string][]PromptAttachment

	mu       sync.Mutex
	uploads  []memoryUpload
	sessions map[string]map[string]memoryUpload
}

type memoryUpload struct {
	sessionID  string
	name       string
	data       []byte
	descriptor AttachmentDescriptor
}

func newMemoryAttachments() *memoryAttachments {
	return &memoryAttachments{
		sessions: map[string]map[string]memoryUpload{},
		prompts:  map[string][]PromptAttachment{},
	}
}

func (m *memoryAttachments) Save(_ context.Context, sessionID, name string, data []byte) (AttachmentDescriptor, error) {
	descriptor := describeBytes(name, data)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.uploads = append(m.uploads, memoryUpload{sessionID: sessionID, name: name, data: append([]byte(nil), data...), descriptor: descriptor})
	if m.sessions[sessionID] == nil {
		m.sessions[sessionID] = map[string]memoryUpload{}
	}
	m.sessions[sessionID][descriptor.ID] = memoryUpload{sessionID: sessionID, name: name, data: append([]byte(nil), data...)}
	return descriptor, nil
}

// RecordPromptAttachments keeps what one prompt carried, so a test can assert the
// seam the projector reads.
func (m *memoryAttachments) RecordPromptAttachments(_ context.Context, sessionID, runID string, attachments []PromptAttachment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.prompts == nil {
		m.prompts = map[string][]PromptAttachment{}
	}
	m.prompts[runID] = append([]PromptAttachment(nil), attachments...)
	return nil
}

// MaterializePromptFile publishes a copy in the fake tool world and returns its
// path, which is what the model is told to read.
func (m *memoryAttachments) MaterializePromptFile(_ context.Context, sessionID, attachmentID string) (string, error) {
	descriptor, _, err := m.Read(context.Background(), sessionID, attachmentID)
	if err != nil {
		return "", err
	}
	name := descriptor.Name
	if name == "" {
		name = attachmentID
	}
	return "attachments/" + name, nil
}

func (m *memoryAttachments) Read(_ context.Context, sessionID, attachmentID string) (AttachmentDescriptor, []byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	stored, ok := m.sessions[sessionID][attachmentID]
	if !ok {
		return AttachmentDescriptor{}, nil, ErrAttachmentNotFound
	}
	return describeBytes(stored.name, stored.data), stored.data, nil
}

func (m *memoryAttachments) saved() []memoryUpload {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]memoryUpload(nil), m.uploads...)
}

// savedID is the id this store published for one name, so a test can assert the
// descriptor the projector is handed is the one the console was given.
func (m *memoryAttachments) savedID(t *testing.T, sessionID, name string) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, upload := range m.uploads {
		if upload.sessionID == sessionID && upload.name == name {
			return upload.descriptor.ID
		}
	}
	t.Fatalf("no stored attachment named %q for %q", name, sessionID)
	return ""
}

// PromptAttachments is the read half a host installs on its stream.
func (m *memoryAttachments) PromptAttachments(_ context.Context, runID string) ([]PromptAttachment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]PromptAttachment(nil), m.prompts[runID]...), nil
}

func (m *memoryAttachments) mustSave(t *testing.T, sessionID, name string, data []byte) AttachmentDescriptor {
	t.Helper()
	descriptor, err := m.Save(context.Background(), sessionID, name, data)
	if err != nil {
		t.Fatalf("save %s: %v", name, err)
	}
	return descriptor
}

func (m *memoryAttachments) mustHave(t *testing.T, sessionID, attachmentID string, want []byte) {
	t.Helper()
	_, data, err := m.Read(context.Background(), sessionID, attachmentID)
	if err != nil {
		t.Fatalf("read %s: %v", attachmentID, err)
	}
	if !bytes.Equal(data, want) {
		t.Fatalf("stored bytes = %q, want %q", data, want)
	}
}

// describeBytes is the store's own descriptor derivation, shared by the save and
// read paths so both describe the same bytes the same way.
func describeBytes(name string, data []byte) AttachmentDescriptor {
	mediaType := model.DetectImageMediaType(data)
	width, height := 0, 0
	if mediaType != "" {
		if w, h, err := model.ImageSize(mediaType, data); err == nil {
			width, height = w, h
		}
	}
	return AttachmentDescriptor{
		ID:        attachmentIDFor(data),
		Name:      name,
		MediaType: mediaType,
		Bytes:     len(data),
		Width:     width,
		Height:    height,
	}
}

// decodeJSONInto decodes a method's raw result value into a test struct.
func decodeJSONInto(t *testing.T, raw json.RawMessage, target any) {
	t.Helper()
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("decode result %s: %v", raw, err)
	}
}

// attachmentIDFor derives the id the way a store does: a prefix of the digest,
// so two uploads of the same bytes are the same attachment.
func attachmentIDFor(data []byte) string {
	digest := sha256.Sum256(data)
	return "att-" + hex.EncodeToString(digest[:])[:32]
}

// encodePNG builds a real PNG of the given size, so dimensions in these tests are
// read from an image header rather than asserted from a constant.
func encodePNG(t *testing.T, width, height int) []byte {
	t.Helper()
	pixels := image.NewRGBA(image.Rect(0, 0, width, height))
	pixels.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, pixels); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buffer.Bytes()
}

func (f *fixture) postRawUpload(t *testing.T, sessionID, name string, data []byte) *bytes.Buffer {
	t.Helper()
	path := fmt.Sprintf("/api/session/uploadFileBinary?sessionId=%s&name=%s", sessionID, name)
	request := httptest.NewRequest("POST", path, bytes.NewReader(data))
	request.Header.Set("Content-Type", "application/octet-stream")
	// The trust fence refuses a non-loopback caller, and httptest's default
	// RemoteAddr is a documentation address rather than a loopback one.
	request.RemoteAddr = "127.0.0.1:54321"
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, request)
	return recorder.Body
}

// decodeRawUpload decodes the raw route's `{ok, value|error}` body.
func decodeRawUpload(t *testing.T, body *bytes.Buffer) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode raw upload answer %s: %v", body.String(), err)
	}
	return decoded
}

func (f *fixture) lastTask(t *testing.T) zenforge.Task {
	t.Helper()
	f.agent.mu.Lock()
	defer f.agent.mu.Unlock()
	if len(f.agent.tasks) == 0 {
		t.Fatalf("no task was started")
	}
	return f.agent.tasks[len(f.agent.tasks)-1]
}

// The seam between the host that admitted a prompt and the projector that shows
// its turn: what the prompt carried is recorded under the run that turn is, and
// the console's stream reads it back by run id.
func TestPromptAttachmentsReachTheProjectedMessage(t *testing.T) {
	f := newFixture(t, Config{})
	store := newMemoryAttachments()
	f.handler.SetAttachments(store)
	sessionID := f.createSession(t)

	encoded := encodePNG(t, 4, 5)
	envelope := decodeResponse(t, f.post(t, "/api/session/prompt", rpcBody(t, "rpc-prompt", "session/prompt",
		`{"requestId":"req-1","sessionId":`+mustJSON(t, sessionID)+
			`,"mode":"queue","content":[{"type":"text","text":"what is this"},`+
			`{"type":"image","mediaType":"image/png","data":`+mustJSON(t, base64.StdEncoding.EncodeToString(encoded))+`,"name":"pixels.png"}]}`)))
	if !envelope.Result.OK {
		t.Fatalf("prompt = %s, want it accepted", envelope.Result.Error)
	}
	task := f.lastTask(t)
	expected := dshwire.Attachment{
		Kind:         "image",
		AttachmentID: store.savedID(t, sessionID, "pixels.png"),
		Name:         "pixels.png",
		MediaType:    "image/png",
		Bytes:        len(encoded),
		Width:        4,
		Height:       5,
	}
	recorded := store.prompts[task.RunID]
	if len(recorded) != 1 {
		t.Fatalf("recorded for %q = %+v, want one attachment", task.RunID, recorded)
	}
	if recorded[0].Kind != "image" || recorded[0].AttachmentID != expected.AttachmentID ||
		recorded[0].Width != 4 || recorded[0].Height != 5 || recorded[0].Bytes != len(encoded) {
		t.Fatalf("recorded = %+v, want %+v", recorded[0], expected)
	}
	// The projector's half answers the same thing for that run, and nothing for
	// another.
	inputs := PromptInputAttachments(func(runID string) []PromptAttachment {
		recorded, _ := store.PromptAttachments(context.Background(), runID)
		return recorded
	})
	attachments := inputs(task.RunID)
	if len(attachments) != 1 || attachments[0] != expected {
		t.Fatalf("projector inputs = %+v, want %+v", attachments, expected)
	}
	if other := inputs(task.RunID + "~9"); len(other) != 0 {
		t.Fatalf("another run = %+v, want none", other)
	}
	if PromptInputAttachments(nil) != nil {
		t.Fatal("no source must project no attachments")
	}
}

// An image whose header this host cannot measure cannot be described to the
// console, whose message block carries the dimensions a viewer draws at: the
// prompt is refused by name rather than shown as an attachment of unknown size.
func TestPromptRefusesAnImageItCannotMeasure(t *testing.T) {
	f := newFixture(t, Config{})
	f.handler.SetAttachments(newMemoryAttachments())
	sessionID := f.createSession(t)

	broken := decodeFixture(t, testWebPBase64)[:20]
	envelope := decodeResponse(t, f.post(t, "/api/session/prompt", rpcBody(t, "rpc-prompt", "session/prompt",
		`{"requestId":"req-1","sessionId":`+mustJSON(t, sessionID)+
			`,"mode":"queue","content":[{"type":"text","text":"what is this"},`+
			`{"type":"image","mediaType":"image/webp","data":`+mustJSON(t, base64.StdEncoding.EncodeToString(broken))+`}]}`)))
	if envelope.Result.OK || envelope.Result.Error.Code != attachmentInvalidCode {
		t.Fatalf("broken image prompt = %+v, want %q", envelope.Result, attachmentInvalidCode)
	}
	if reason, _ := envelope.Result.Error.Details["reason"].(string); reason != "DIMENSIONS_UNREADABLE" {
		t.Fatalf("details = %+v, want DIMENSIONS_UNREADABLE", envelope.Result.Error.Details)
	}
	if !strings.Contains(envelope.Result.Error.Message, "image/webp") {
		t.Fatalf("message = %q, want the format named", envelope.Result.Error.Message)
	}
}

// A host with no store refuses a prompt's attachment with the family's own
// sentence, and starts no turn: an attachment the transcript could not refer to
// is not admitted on the promise of a later error.
func TestPromptWithoutAStoreRefusesAttachmentsByName(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.createSession(t)
	encoded := encodePNG(t, 2, 2)
	envelope := decodeResponse(t, f.post(t, "/api/session/prompt", rpcBody(t, "rpc-prompt", "session/prompt",
		`{"requestId":"req-1","sessionId":`+mustJSON(t, sessionID)+
			`,"mode":"queue","content":[{"type":"text","text":"what is this"},`+
			`{"type":"image","mediaType":"image/png","data":`+mustJSON(t, base64.StdEncoding.EncodeToString(encoded))+`}]}`)))
	if envelope.Result.OK || envelope.Result.Error.Code != codeUnimplemented {
		t.Fatalf("prompt without a store = %+v, want %q", envelope.Result, codeUnimplemented)
	}
	if envelope.Result.Error.Message != attachmentRefusal {
		t.Fatalf("message = %q, want the family's own sentence", envelope.Result.Error.Message)
	}
	if _, ok := envelope.Result.Error.Details["capability"]; !ok {
		t.Fatalf("details = %+v, want the missing capability named", envelope.Result.Error.Details)
	}
}
