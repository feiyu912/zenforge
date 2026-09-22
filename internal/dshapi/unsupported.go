package dshapi

import (
	"context"
	"encoding/json"
)

// The console's client declares namespaces whose capability this host does not
// have. Each one is *refused by name*: the method is routed, validates nothing it
// cannot act on, and answers `unimplemented` with the reason and the substitute,
// so a panel says "this host cannot do that, here is what it does instead" rather
// than showing an empty answer that reads as success (ADR 0082, ADR 0128).
//
// One handler covers a whole family because the family shares one reason. That is
// the same shape `pluginManagerUnsupported` uses; the alternative -- a handler per
// method with the same sentence -- would let the sentences drift apart.

// TerminalRefusal is the reason the `terminal/*` family is refused. The host does
// run commands under a PTY for its own tools (`jobs.Manager`), which is exactly why
// the refusal has to be precise: what is missing is the console-facing half -- an
// attachment layer, a runtime resize, and a screen model for `follow` -- not the
// ability to run a command.
const TerminalRefusal = "this host runs commands under a PTY for its own tools, but it has no terminal attachment layer, no runtime resize and no screen model, so an embedded terminal cannot be served; use the agent's own shell and job tools instead"

// SubagentRefusal is the reason the `subagents/*` family is refused. The follow
// stream's `subagent` address arm refuses for the same reason, in the same words'
// spirit, so the two halves cannot tell different stories.
const SubagentRefusal = "this host runs subagents as tasks inside the parent's own run and registers no child session, so there is no child to list, prompt or interrupt; session/follow refuses subagent addresses for the same reason"

// SessionReferenceRefusal is the reason
// `sessionReferenceResolver/candidates` is refused. The file half of the same `@`
// menu *is* served, so the substitute points at it.
const SessionReferenceRefusal = "this host does not parse or expand an @-mention, so a picked conversation reference would reach the model as literal text; the file section of the same menu is served by fileReferences/list"

// OfficeToPdfRefusal is the reason the `officeToPdf/*` family is refused.
const OfficeToPdfRefusal = "this host has no Office document converter and no PDF renderer, and the console bundle that would display the result is dropped from this build; workspaceFiles/readAll serves the document preview's byte arm instead"

// DynamicCordisRunnerRefusal is the reason the `dynamicCordisRunner/*` family is
// refused. It is the same reason the plugin-manager namespace gives: this host's
// plugins are compiled in.
const DynamicCordisRunnerRefusal = "this host ships a fixed set of console bundles and has no dynamic plugin runtime, so nothing in this namespace could be run, inspected or settled"

// terminalUnsupported refuses every `terminal/*` method.
func (h *Handler) terminalUnsupported(_ context.Context, _ map[string]json.RawMessage) (any, *methodError) {
	return nil, fail(codeUnimplemented, TerminalRefusal,
		map[string]any{"capability": "an embedded terminal"})
}

// subagentsUnsupported refuses every `subagents/*` method.
func (h *Handler) subagentsUnsupported(_ context.Context, _ map[string]json.RawMessage) (any, *methodError) {
	return nil, fail(codeUnimplemented, SubagentRefusal,
		map[string]any{"capability": "a child-session plane"})
}

// sessionReferenceResolverUnsupported refuses the resolver's candidate list.
func (h *Handler) sessionReferenceResolverUnsupported(_ context.Context, _ map[string]json.RawMessage) (any, *methodError) {
	return nil, fail(codeUnimplemented, SessionReferenceRefusal,
		map[string]any{"capability": "a session reference resolver"})
}

// officeToPdfUnsupported refuses both halves of the document conversion.
func (h *Handler) officeToPdfUnsupported(_ context.Context, _ map[string]json.RawMessage) (any, *methodError) {
	return nil, fail(codeUnimplemented, OfficeToPdfRefusal,
		map[string]any{"capability": "an Office document converter"})
}

// dynamicCordisRunnerUnsupported refuses the whole dynamic-plugin runner.
func (h *Handler) dynamicCordisRunnerUnsupported(_ context.Context, _ map[string]json.RawMessage) (any, *methodError) {
	return nil, fail(codeUnimplemented, DynamicCordisRunnerRefusal,
		map[string]any{"capability": "a dynamic plugin runtime"})
}

// fileUploadsUnsupported refuses the console's upload route with the attachment
// sentence the read half already uses, verbatim: the two are the same missing
// store, and a caller that met one must not meet a different story at the other.
func (h *Handler) fileUploadsUnsupported(_ context.Context, _ map[string]json.RawMessage) (any, *methodError) {
	return nil, fail(codeUnimplemented, attachmentRefusal,
		map[string]any{"capability": "an attachment store"})
}
