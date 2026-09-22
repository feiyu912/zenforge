package dshapi

import (
	"strings"
	"testing"
)

// The attachment seam's read half is pinned against the vendored bytes and to the
// half that already refuses: its two declared fields arrive flattened from the
// request object, and a well-formed call is refused because the store is missing,
// not the argument.
func TestSessionAttachmentEnvelopeMatchesTheVendoredConsole(t *testing.T) {
	console := sessionBundle(t)
	descriptor := console.descriptor(t, "attachment")
	wires := console.wireNames(t, descriptor, "attachment")
	if !sameStrings(wires, []string{"request"}) {
		t.Fatalf("session/attachment wires = %v, want the request object", wires)
	}
	objects := console.objectParameters(t, "attachment")
	if len(objects) != 1 {
		t.Fatalf("session/attachment object parameters = %v, want one", objects)
	}
	if keys := sortedKeys(objects[0]); !sameStrings(keys, []string{"attachmentId", "sessionId"}) {
		t.Fatalf("session/attachment request keys = %v, want the session and attachment ids", keys)
	}
	// The read half answers the metadata and the bytes, which is why refusing it
	// is the honest answer rather than answering metadata-only.
	result := console.schemaExpression(t, "attachment", "result")
	if keys := sortedKeys(topLevelKeys(t, result)); !sameStrings(keys, []string{"attachment", "data"}) {
		t.Fatalf("session/attachment result keys = %v, want the attachment and its data", keys)
	}
	if !strings.Contains(result, `"mediaType"`) || !strings.Contains(result, "image/png") {
		t.Fatalf("session/attachment result = %q, want the image metadata the console reads", result)
	}
	if accepted := handlerArgumentNames(t, readSource(t, "attachment.go"), "sessionAttachment"); !sameStrings(accepted, []string{"sessionId", "attachmentId"}) {
		t.Fatalf("sessionAttachment accepts %v, want the flattened request keys", accepted)
	}
}

func TestSessionAttachmentNamesTheMissingCapability(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.createSession(t)
	for _, args := range []string{
		`{"sessionId":` + jsonString(sessionID) + `,"attachmentId":"att-1"}`,
		`{"request":{"sessionId":` + jsonString(sessionID) + `,"attachmentId":"att-1"}}`,
	} {
		envelope := decodeResponse(t, f.post(t, "/api/session/attachment",
			rpcBody(t, "s1", "session/attachment", args)))
		if envelope.Result.OK {
			t.Fatalf("%s: result = %s, want a refusal", args, envelope.Result.Value)
		}
		if envelope.Result.Error.Code != codeUnimplemented {
			t.Fatalf("%s: code = %q, want %q", args, envelope.Result.Error.Code, codeUnimplemented)
		}
		if capability, _ := envelope.Result.Error.Details["capability"].(string); capability != "an attachment store" {
			t.Fatalf("%s: details = %+v, want the missing store named", args, envelope.Result.Error.Details)
		}
		if !strings.Contains(envelope.Result.Error.Message, "workspace") {
			t.Fatalf("%s: message = %q, want the substitute named", args, envelope.Result.Error.Message)
		}
	}
	// The typo is reported as a typo, not as this host's missing store.
	envelope := decodeResponse(t, f.post(t, "/api/session/attachment",
		rpcBody(t, "s1", "session/attachment",
			`{"sessionId":`+jsonString(sessionID)+`,"attachmentId":"att-1","mediaType":"image/png"}`)))
	if envelope.Result.OK || envelope.Result.Error.Code != codeArgumentsInvalid {
		t.Fatalf("result = %+v, want an arguments-invalid refusal", envelope.Result)
	}
}

// Both halves of the seam answer the same way, which is the point of refusing the
// read: an attachment cannot be submitted to this host either.
func TestPromptRefusesTheSameAttachmentHalf(t *testing.T) {
	f := newFixture(t, Config{})
	sessionID := f.createSession(t)
	recorder := f.post(t, "/api/session/prompt", rpcBody(t, "rpc-prompt", "session/prompt",
		`{"requestId":"req-1","sessionId":`+jsonString(sessionID)+
			`,"mode":"queue","content":[{"type":"text","text":"look at this"},{"type":"image","attachmentId":"att-1"}]}`))
	envelope := decodeResponse(t, recorder)
	if envelope.Result.OK {
		t.Fatalf("prompt = %s, want the image part refused", envelope.Result.Value)
	}
	if envelope.Result.Error.Code != codeUnsupportedContent {
		t.Fatalf("code = %q, want %q", envelope.Result.Error.Code, codeUnsupportedContent)
	}
	if !strings.Contains(envelope.Result.Error.Message, "text parts only") {
		t.Fatalf("message = %q, want the text-only rule named", envelope.Result.Error.Message)
	}
}
