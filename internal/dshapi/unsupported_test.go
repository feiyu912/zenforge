package dshapi

import (
	"strings"
	"testing"
)

// The refused families are routed on purpose: every method answers a named
// refusal rather than a 404, so the console's own control says what is missing
// instead of looking like a transport failure. This test is the one place the
// routing and the sentences are checked together.
//
// The attachment family left this table when the store landed (ADR 0138): it is
// now served, and a host without a store still answers the same sentence, which
// the attachment family's own test pins.
func TestUnsupportedNamespacesRefuseByName(t *testing.T) {
	f := newFixture(t, Config{})
	cases := []struct {
		method     string
		capability string
		sentence   string
	}{
		{"terminal/close", "an embedded terminal", TerminalRefusal},
		{"terminal/create", "an embedded terminal", TerminalRefusal},
		{"terminal/environment", "an embedded terminal", TerminalRefusal},
		{"terminal/follow", "an embedded terminal", TerminalRefusal},
		{"terminal/list", "an embedded terminal", TerminalRefusal},
		{"terminal/rename", "an embedded terminal", TerminalRefusal},
		{"terminal/resize", "an embedded terminal", TerminalRefusal},
		{"terminal/retain", "an embedded terminal", TerminalRefusal},
		{"terminal/shells", "an embedded terminal", TerminalRefusal},
		{"terminal/write", "an embedded terminal", TerminalRefusal},
		{"subagents/list", "a child-session plane", SubagentRefusal},
		{"subagents/prompt", "a child-session plane", SubagentRefusal},
		{"subagents/interruptByParent", "a child-session plane", SubagentRefusal},
		{"sessionReferenceResolver/candidates", "a session reference resolver", SessionReferenceRefusal},
		{"officeToPdf/generation", "an Office document converter", OfficeToPdfRefusal},
		{"officeToPdf/render", "an Office document converter", OfficeToPdfRefusal},
		{"dynamicCordisRunner/getClientCode", "a dynamic plugin runtime", DynamicCordisRunnerRefusal},
		{"dynamicCordisRunner/inventory", "a dynamic plugin runtime", DynamicCordisRunnerRefusal},
		{"dynamicCordisRunner/invoke", "a dynamic plugin runtime", DynamicCordisRunnerRefusal},
		{"dynamicCordisRunner/reportClientGuardFailure", "a dynamic plugin runtime", DynamicCordisRunnerRefusal},
		{"dynamicCordisRunner/reportRenderFailure", "a dynamic plugin runtime", DynamicCordisRunnerRefusal},
		{"dynamicCordisRunner/resolveInspectQuery", "a dynamic plugin runtime", DynamicCordisRunnerRefusal},
		{"dynamicCordisRunner/resolveRequestRun", "a dynamic plugin runtime", DynamicCordisRunnerRefusal},
		{"dynamicCordisRunner/runHostHalf", "a dynamic plugin runtime", DynamicCordisRunnerRefusal},
		{"dynamicCordisRunner/settleUserRun", "a dynamic plugin runtime", DynamicCordisRunnerRefusal},
		{"dynamicCordisRunner/stopFromPanel", "a dynamic plugin runtime", DynamicCordisRunnerRefusal},
		{"dynamicCordisRunner/syncInspectManifest", "a dynamic plugin runtime", DynamicCordisRunnerRefusal},
		{"dynamicCordisRunner/undefineFromPanel", "a dynamic plugin runtime", DynamicCordisRunnerRefusal},
	}
	for _, testCase := range cases {
		t.Run(testCase.method, func(t *testing.T) {
			recorder := f.post(t, "/api/"+testCase.method, rpcBody(t, "rpc-refused", testCase.method, `{}`))
			envelope := assertMethodFailure(t, recorder, codeUnimplemented)
			if envelope.Result.Error.Message != testCase.sentence {
				t.Fatalf("message = %q, want the family's one sentence", envelope.Result.Error.Message)
			}
			if envelope.Result.Error.Details["capability"] != testCase.capability {
				t.Fatalf("details = %+v, want capability %q", envelope.Result.Error.Details, testCase.capability)
			}
		})
	}
}

// The upload route and the attachment read are the same store, so a host without
// one answers both with the same sentence -- which is what the attachment family
// test pins, args and all. What this file still owns is that the *terminal*
// refusal names the missing half rather than the missing PTY.
func TestTerminalRefusalNamesTheMissingHalf(t *testing.T) {
	if !strings.Contains(TerminalRefusal, "no terminal attachment layer") {
		t.Fatal("the terminal refusal must name the missing half, not the missing PTY")
	}
}

// A method the console does not declare is still a 404: the refusals name
// capabilities, they do not turn a namespace into a catch-all.
func TestUnsupportedNamespacesKeepUnknownMethodsUnknown(t *testing.T) {
	f := newFixture(t, Config{})
	for _, endpoint := range []string{"terminal/detach", "subagents/spawn", "dynamicCordisRunner/dance", "fileUploads/download"} {
		recorder := f.post(t, "/api/"+endpoint, rpcBody(t, "rpc-unknown", endpoint, `{}`))
		if recorder.Code != 404 {
			t.Fatalf("%s answered %d, want 404 for an undeclared method", endpoint, recorder.Code)
		}
	}
}
