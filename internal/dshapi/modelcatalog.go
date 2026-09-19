package dshapi

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// Wire shapes for POST /api/session/modelCatalog, the catalog the console's
// model selector loads once per host generation
// (packages/client/ui-model-selection/src/client/catalog.ts:36-66). Without
// the endpoint the picker renders its failure state while chat still works.
//
// The declarations below mirror the upstream session-controller types, cited
// by path:line. The authoritative block is
// packages/api/session-controller/src/types.ts:144-150 for ModelCatalog, which
// this repository's docs/dsh-console-protocol-recon.md cites as types.ts:144-150;
// the surrounding items are types.ts:86-90 (ModelSelection), :109-113
// (ModelReasoningEffort), :116-119 (ModelReasoning), :122-127
// (ModelCatalogModel), :130-134 (ModelProviderGroup), and :137-141
// (ModelCatalogFailure). The semantics of each field are the ones the host's
// own builder produces in packages/api/session-controller/src/catalog.ts:60-66.
//
// The value cannot be read from cli/serve.go's settings store here without an
// import cycle, so it is injected: SetModelCatalog installs a
// ModelCatalogSource and sessionModelCatalog answers from it. No source means
// an honest unimplemented method error, never a fabricated catalog.

// ModelSelection is the provider/model pair a session uses, plus the optional
// adapter-owned reasoning effort. Upstream types.ts:86-90.
type ModelSelection struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// ReasoningEffort is optional upstream (types.ts:89); omitted when the
	// adapter exposes no effort choice.
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
}

// ModelReasoningEffort is one selectable reasoning effort. Upstream
// types.ts:109-113.
type ModelReasoningEffort struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Description is optional upstream (types.ts:112).
	Description string `json:"description,omitempty"`
}

// ModelReasoning is the selectable reasoning metadata for one model.
// Upstream types.ts:116-119.
type ModelReasoning struct {
	Efforts []ModelReasoningEffort `json:"efforts"`
	// DefaultEffort is optional upstream (types.ts:118).
	DefaultEffort string `json:"defaultEffort,omitempty"`
}

// ModelCatalogModel is one model displayed inside its provider group.
// Upstream types.ts:122-127.
type ModelCatalogModel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Description and Reasoning are optional upstream (types.ts:125-126).
	Description string          `json:"description,omitempty"`
	Reasoning   *ModelReasoning `json:"reasoning,omitempty"`
}

// ModelProviderGroup is one provider and its successfully loaded models.
// Upstream types.ts:130-134.
type ModelProviderGroup struct {
	ID     string              `json:"id"`
	Name   string              `json:"name"`
	Models []ModelCatalogModel `json:"models"`
}

// ModelCatalogFailure is one provider whose model catalog lookup failed.
// Upstream types.ts:137-141.
type ModelCatalogFailure struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Message string `json:"message"`
}

// ModelCatalog is the host-generation catalog and the default used by
// unconfigured sessions. Upstream types.ts:144-150.
type ModelCatalog struct {
	// Default is the selection used before a session chooses a model. It is a
	// value, not a pointer: the wire type declares it required.
	Default ModelSelection `json:"default"`
	// RoutableProviders lists the routes currently able to serve a request,
	// including providers with empty catalogs. Required on the wire, so it is
	// written even when empty.
	RoutableProviders []string `json:"routableProviders"`
	// Groups holds only the providers with at least one loaded model.
	Groups []ModelProviderGroup `json:"groups"`
	// Failures holds the providers whose catalog lookup failed.
	Failures []ModelCatalogFailure `json:"failures"`
}

// ModelCatalogSource reports the host's configured model catalog. It is a
// function type rather than an interface so that nil unambiguously means "no
// source installed" — an interface holding a typed nil would pass a nil check
// and then panic on the call. The serve command injects a closure over its
// settings store; this package cannot import cli/serve.go, which would cycle.
type ModelCatalogSource func() ModelCatalog

// SetModelCatalog installs the source sessionModelCatalog answers from. It is
// deliberately separate from New so the serve command can supply its settings
// store later without changing New's signature, and it may be called again
// when the operator changes the model. Passing nil removes the source and
// restores the unimplemented answer. Safe to call while requests are in
// flight.
func (h *Handler) SetModelCatalog(source ModelCatalogSource) {
	h.modelCatalogMu.Lock()
	h.modelCatalog = source
	h.modelCatalogMu.Unlock()
}

// modelCatalogSource is the installed catalog source, or nil when none is.
func (h *Handler) modelCatalogSource() ModelCatalogSource {
	h.modelCatalogMu.RLock()
	defer h.modelCatalogMu.RUnlock()
	return h.modelCatalog
}

// sessionModelCatalog answers POST /api/session/modelCatalog. The upstream
// method takes no arguments, and the gateway rejects any unexpected field as
// gateway/arguments-invalid (packages/api/gateway/src/index.ts:1107-1132);
// this method applies the same exact-arguments rule.
//
// With no injected source there is no honest catalog to return, so the reply
// is an unimplemented method error naming the missing dependency. A selector
// panel that shows an error is a better failure than one that shows a model
// this host is not configured with, which is this package's established rule.
func (h *Handler) sessionModelCatalog(_ context.Context, args map[string]json.RawMessage) (any, *methodError) {
	if failure := rejectUnexpectedArguments("session/modelCatalog", args); failure != nil {
		return nil, failure
	}
	source := h.modelCatalogSource()
	if source == nil {
		return nil, fail(codeUnimplemented,
			"session/modelCatalog is not configured: the host has no model catalog source; the serve command must install one with Handler.SetModelCatalog",
			map[string]any{"dependency": "ModelCatalogSource"})
	}
	return source().normalized(), nil
}

// rejectUnexpectedArguments enforces the upstream exact-arguments rule for a
// method that takes none. It names one offending field and never echoes its
// value; the field name is sorted so a request with several extras logs and
// reports deterministically.
func rejectUnexpectedArguments(method string, args map[string]json.RawMessage) *methodError {
	if len(args) == 0 {
		return nil
	}
	names := make([]string, 0, len(args))
	for name := range args {
		names = append(names, name)
	}
	sort.Strings(names)
	return fail(codeArgumentsInvalid,
		fmt.Sprintf("%s takes no arguments: unexpected %q", method, names[0]),
		map[string]any{"argument": names[0]})
}

// normalized guarantees every array field the wire type requires is present as
// an array rather than null, because a nil slice marshals to null and the
// console's type declares these fields as readonly T[]. It copies rather than
// mutates, so a source that reuses one catalog value cannot observe the
// change, and it normalizes the nested groups[].models and reasoning.efforts
// arrays the same way.
func (catalog ModelCatalog) normalized() ModelCatalog {
	normalized := catalog
	normalized.RoutableProviders = append([]string{}, catalog.RoutableProviders...)
	normalized.Failures = append([]ModelCatalogFailure{}, catalog.Failures...)
	normalized.Groups = make([]ModelProviderGroup, len(catalog.Groups))
	for index, group := range catalog.Groups {
		group.Models = append([]ModelCatalogModel{}, group.Models...)
		for modelIndex := range group.Models {
			reasoning := group.Models[modelIndex].Reasoning
			if reasoning == nil {
				continue
			}
			efforts := *reasoning
			efforts.Efforts = append([]ModelReasoningEffort{}, reasoning.Efforts...)
			group.Models[modelIndex].Reasoning = &efforts
		}
		normalized.Groups[index] = group
	}
	return normalized
}
