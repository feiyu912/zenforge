package dshapi

import (
	"strings"
	"testing"
)

// sessionRemotePath is the same generated remote map the goal tests read: the two
// desktop methods live in the session controller's package.
const sessionRemotePath = "../../webui/dsh/plugins/api/remotes/client.js"

// sessionBundle is the generated declaration of the session controller's package.
func sessionBundle(t *testing.T) vendoredBundle {
	t.Helper()
	return vendoredBundle{source: readSource(t, sessionRemotePath), pkg: "@deepseek-ai/dsh-api-session-controller", ns: "session"}
}

// The desktop half of the session namespace is a question and an operation, and
// both are pinned against the vendored bytes: the probe's result is a *bare*
// boolean, the operation's request is one object that arrives flattened, and this
// host's handlers must accept exactly the names the client sends. A regression to
// `{"opened":false}` for the probe, or to an object result, would be invisible to
// a test that only checked the answer's truthiness.
func TestWorkspaceDesktopEnvelopesMatchTheVendoredConsole(t *testing.T) {
	console := sessionBundle(t)
	recipe := readSource(t, "workspaceopen.go")

	// The probe takes no parameters and answers a boolean.
	if wires := console.descriptor(t, "canOpenWorkspacePath"); !strings.Contains(wires, "parameters: []") {
		t.Fatalf("session/canOpenWorkspacePath declares parameters: %s", wires)
	}
	probeResult := console.schemaExpression(t, "canOpenWorkspacePath", "result")
	if !strings.Contains(probeResult, "boolean()") || strings.Contains(probeResult, "object(") {
		t.Fatalf("the probe result schema = %q, want a bare boolean", strings.TrimSpace(probeResult))
	}
	if keys := topLevelKeys(t, probeResult); len(keys) != 0 {
		t.Fatalf("the probe result declares object keys %v, want none", keys)
	}
	if !strings.Contains(recipe, `rejectUnexpectedArguments("session/canOpenWorkspacePath", args)`) {
		t.Fatal("the probe does not enforce the exact-arguments rule for a zero-argument method")
	}

	// The operation takes one `request` object and answers `{opened: true}`.
	descriptor := console.descriptor(t, "openWorkspacePath")
	wires := console.wireNames(t, descriptor, "openWorkspacePath")
	if !sameStrings(wires, []string{"request"}) {
		t.Fatalf("session/openWorkspacePath wires = %v, want the request object", wires)
	}
	objects := console.objectParameters(t, "openWorkspacePath")
	if len(objects) != 1 {
		t.Fatalf("session/openWorkspacePath object parameters = %v, want one", objects)
	}
	if keys := sortedKeys(objects[0]); !sameStrings(keys, []string{"action", "path"}) {
		t.Fatalf("session/openWorkspacePath request keys = %v, want action and path", keys)
	}
	result := console.schemaExpression(t, "openWorkspacePath", "result")
	if keys := sortedKeys(topLevelKeys(t, result)); !sameStrings(keys, []string{"opened"}) {
		t.Fatalf("session/openWorkspacePath result keys = %v, want opened", keys)
	}
	if !strings.Contains(result, `literal(true)`) {
		t.Fatalf("session/openWorkspacePath result = %q, want the literal true receipt", result)
	}

	// What this host's handler accepts, read from its own source.
	accepted := handlerArgumentNames(t, recipe, "sessionOpenWorkspacePath")
	if !sameStrings(accepted, []string{"path", "action"}) {
		t.Fatalf("sessionOpenWorkspacePath accepts %v, want the flattened request keys", accepted)
	}
}

// The probe has one honest answer here, and it must be the JSON boolean rather
// than an object that happens to decode to false.
func TestCanOpenWorkspacePathAnswersABoolean(t *testing.T) {
	f := newFixture(t, Config{})
	envelope := decodeResponse(t, f.post(t, "/api/session/canOpenWorkspacePath",
		rpcBody(t, "s1", "session/canOpenWorkspacePath", `{}`)))
	if !envelope.Result.OK {
		t.Fatalf("result = %+v, want an answer", envelope.Result.Error)
	}
	if got := strings.TrimSpace(string(envelope.Result.Value)); got != "false" {
		t.Fatalf("value = %s, want the bare boolean false", got)
	}
}

// A valid request is refused too, because what is missing is the host's desktop
// and not an argument: the refusal has to name the capability so the console can
// tell "this deployment cannot" from "you asked wrongly".
func TestOpenWorkspacePathNamesTheMissingCapability(t *testing.T) {
	f := newFixture(t, Config{})
	for _, args := range []string{
		`{"path":"/tmp"}`,
		`{"path":"/tmp","action":"reveal"}`,
		`{"request":{"path":"/tmp","action":"reveal"}}`,
	} {
		envelope := decodeResponse(t, f.post(t, "/api/session/openWorkspacePath",
			rpcBody(t, "s1", "session/openWorkspacePath", args)))
		if envelope.Result.OK {
			t.Fatalf("%s: result = %s, want a refusal", args, envelope.Result.Value)
		}
		if envelope.Result.Error.Code != codeUnimplemented {
			t.Fatalf("%s: code = %q, want %q", args, envelope.Result.Error.Code, codeUnimplemented)
		}
		if capability, _ := envelope.Result.Error.Details["capability"].(string); capability == "" {
			t.Fatalf("%s: details = %+v, want the missing capability named", args, envelope.Result.Error.Details)
		}
		if !strings.Contains(envelope.Result.Error.Message, "desktop") {
			t.Fatalf("%s: message = %q, want the reason", args, envelope.Result.Error.Message)
		}
	}
}

// The refusal is not a place to stop validating arguments: a field the method
// does not declare is still reported as the typo it is, on both halves.
func TestWorkspaceDesktopRejectsUnknownArguments(t *testing.T) {
	f := newFixture(t, Config{})
	for _, testCase := range []struct{ method, args string }{
		{"session/canOpenWorkspacePath", `{"path":"/tmp"}`},
		{"session/openWorkspacePath", `{"path":"/tmp","reveal":true}`},
	} {
		envelope := decodeResponse(t, f.post(t, "/api/"+testCase.method,
			rpcBody(t, "s1", testCase.method, testCase.args)))
		if envelope.Result.OK || envelope.Result.Error.Code != codeArgumentsInvalid {
			t.Fatalf("%s: result = %+v, want an arguments-invalid refusal", testCase.method, envelope.Result)
		}
	}
}
