package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrResourceNotFound tells the server that a resource handler cannot serve
// the requested URI. It exists because the client has to be able to tell "this
// server has no such resource" from "the resource exists but reading it
// failed": only the first is the spec's resource-not-found error, and a
// handler that returns it gets that code instead of the catch-all internal
// error.
var ErrResourceNotFound = errors.New("mcp resource not found")

// ResourceContent is one item of a resources/read result. Text is for a
// textual resource and Blob is for base64-encoded binary, which is the pair
// the spec defines; a handler sets exactly one of them.
type ResourceContent struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

// ResourceHandler reads one resource. The URI it receives is the concrete one
// the client asked for, not the registered pattern, so a handler behind a
// template can tell which instance was requested. A returned error is a
// JSON-RPC error, not a tool result: resources are not tools, and wrapping a
// read failure in `{content, isError}` would make a client treat a protocol
// failure as a model-visible message. Return ErrResourceNotFound (wrapped) to
// get the spec's resource-not-found code.
type ResourceHandler func(ctx context.Context, uri string) ([]ResourceContent, error)

// ServerResource is one resource the server exposes.
//
// URI is both the identifier and, when a path segment is written as {name}, a
// template: `zenforge://runs/{runId}` is registered once and serves every
// recorded run, which is what lets a server expose a growing collection
// without enumerating it at startup. A URI with no placeholder matches only
// itself, and an exact registration always wins over a template that would
// also match.
type ServerResource struct {
	URI         string
	Name        string
	Description string
	MimeType    string
	Handler     ResourceHandler
}

// resourceMatches reports whether a registered URI pattern serves a concrete
// URI. The rules are deliberately tiny: an exact string always matches, and a
// segment written exactly as {name} matches exactly one non-empty segment.
// Anything more (wildcards, regular expressions) would be a matching language
// no client can discover from resources/list.
func resourceMatches(pattern, uri string) bool {
	if pattern == uri {
		return true
	}
	patternParts := strings.Split(pattern, "/")
	uriParts := strings.Split(uri, "/")
	if len(patternParts) != len(uriParts) {
		return false
	}
	placeholders := false
	for index, part := range patternParts {
		if isResourcePlaceholder(part) {
			placeholders = true
			if uriParts[index] == "" {
				return false
			}
			continue
		}
		if part != uriParts[index] {
			return false
		}
	}
	return placeholders
}

// isResourcePlaceholder reports whether one path segment is a {name}
// placeholder. An empty name is not a placeholder: it would match every
// segment while naming nothing, which is the shape a typo takes.
func isResourcePlaceholder(segment string) bool {
	if len(segment) < 3 || segment[0] != '{' || segment[len(segment)-1] != '}' {
		return false
	}
	return strings.TrimSpace(segment[1:len(segment)-1]) != ""
}

// matchResource returns the registered resource that serves uri. It is the
// gate that keeps resources/read from calling a handler with a URI nobody
// registered: an exact registration wins over a template, then templates are
// tried in registration order, and a URI that matches nothing is refused with
// the spec's resource-not-found code rather than handed to a handler.
func (s *Server) matchResource(uri string) (ServerResource, bool) {
	if resource, ok := s.resourceByURI[uri]; ok {
		return resource, true
	}
	for _, resource := range s.resources {
		if resourceMatches(resource.URI, uri) {
			return resource, true
		}
	}
	return ServerResource{}, false
}

// listResources renders the registered set. It lists a template URI as-is: the
// client discovers that the collection exists, and a read of a concrete
// instance is what the template serves.
func (s *Server) listResources() (json.RawMessage, *rpcError) {
	resources := make([]map[string]any, 0, len(s.resources))
	for _, serverResource := range s.resources {
		entry := map[string]any{
			"uri":  serverResource.URI,
			"name": serverResource.Name,
		}
		if serverResource.Description != "" {
			entry["description"] = serverResource.Description
		}
		if serverResource.MimeType != "" {
			entry["mimeType"] = serverResource.MimeType
		}
		resources = append(resources, entry)
	}
	return mustMarshal(map[string]any{"resources": resources}), nil
}

// isResourceTemplate reports whether a registered URI is a template: at least
// one of its path segments is a {name} placeholder. It asks the same question
// resourceMatches asks of a pattern, using the same predicate, so
// resources/templates/list and the matcher can never disagree about which
// registrations are templates.
func isResourceTemplate(uri string) bool {
	for _, segment := range strings.Split(uri, "/") {
		if isResourcePlaceholder(segment) {
			return true
		}
	}
	return false
}

// listResourceTemplates renders the registered resources that are templates,
// under the spec's uriTemplate key. It exists because a template is not a
// readable resource: resources/list shows the pattern a client cannot read,
// while this list is what tells the client that a whole class of concrete URIs
// is readable and how to call one. A resource with no placeholder belongs in
// resources/list only, so it is excluded here.
//
// params is accepted and ignored on purpose. The spec makes the cursor
// optional and this server does not paginate the registration set: the set is
// fixed for the life of the process, so a cursor would be a promise of a
// "next page" that could only ever be empty. Returning the whole list without
// a nextCursor is the honest answer, and it is also why a malformed params
// object is not rejected -- there is nothing in it this method reads, and
// refusing a request whose only field is one it ignores would turn a harmless
// difference into a failure.
func (s *Server) listResourceTemplates(params json.RawMessage) (json.RawMessage, *rpcError) {
	templates := make([]map[string]any, 0)
	for _, serverResource := range s.resources {
		if !isResourceTemplate(serverResource.URI) {
			continue
		}
		entry := map[string]any{
			"uriTemplate": serverResource.URI,
			"name":        serverResource.Name,
		}
		if serverResource.Description != "" {
			entry["description"] = serverResource.Description
		}
		if serverResource.MimeType != "" {
			entry["mimeType"] = serverResource.MimeType
		}
		templates = append(templates, entry)
	}
	return mustMarshal(map[string]any{"resourceTemplates": templates}), nil
}

// resourceSubscriptionURI reads the uri of a resources/subscribe or
// resources/unsubscribe request. A missing, blank or non-string uri is
// -32602: it is a fact about the request, and there is no resource it could
// name. A non-string value fails inside decodeMessage, which is why the field
// is a plain string rather than a raw message this code would have to police
// itself.
func resourceSubscriptionURI(method string, params json.RawMessage) (string, *rpcError) {
	var request struct {
		URI string `json:"uri"`
	}
	if err := decodeMessage(params, &request); err != nil {
		return "", &rpcError{Code: codeInvalidParams, Message: "invalid " + method + " params: " + err.Error()}
	}
	uri := strings.TrimSpace(request.URI)
	if uri == "" {
		return "", &rpcError{Code: codeInvalidParams, Message: method + " needs a uri"}
	}
	return uri, nil
}

// subscriptionNotAdvertised is the refusal both subscription methods share
// when ServerConfig.ResourceSubscriptions is false. It is JSON-RPC
// method-not-found rather than silence or an invalid-params error: the
// capability block is the server's list of what it serves, subscribe is absent
// from it, and a client calling a method the server never advertised is
// exactly what -32601 says. An error is the right shape rather than silence
// because a client that asked to be told about updates must not be left
// believing it will be; the message names the config field so the server
// author sees the fix.
func (s *Server) subscriptionNotAdvertised(method string) *rpcError {
	name := ""
	if s != nil {
		name = s.name
	}
	return &rpcError{
		Code:    codeMethodNotFound,
		Message: fmt.Sprintf("mcp server %q advertises resources.subscribe false, so %s is not served; set ServerConfig.ResourceSubscriptions to enable it", name, method),
	}
}

// subscribeResource serves resources/subscribe: it records the URI on the
// connection Serve is currently reading. A URI no registration serves is
// -32002, the same answer resources/read gives, because the client asked about
// a resource this server does not have. The subscription is stored as the
// client sent it -- a concrete instance as well as a registered pattern -- so
// NotifyResourceUpdated can match it later the way a read would.
func (s *Server) subscribeResource(params json.RawMessage) (json.RawMessage, *rpcError) {
	if !s.resourceSubscriptions {
		return nil, s.subscriptionNotAdvertised("resources/subscribe")
	}
	uri, rpcErr := resourceSubscriptionURI("resources/subscribe", params)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if _, ok := s.matchResource(uri); !ok {
		return nil, &rpcError{Code: codeResourceNotFound, Message: fmt.Sprintf("unknown resource %q", uri)}
	}
	s.requestMu.Lock()
	if s.subscriptions == nil {
		s.subscriptions = map[string]struct{}{}
	}
	s.subscriptions[uri] = struct{}{}
	s.requestMu.Unlock()
	return mustMarshal(map[string]any{}), nil
}

// unsubscribeResource serves resources/unsubscribe. It is idempotent: a URI
// that is not currently subscribed is removed from a set it was not in and the
// call still succeeds.
//
// That is a deliberate choice, not an oversight. Unsubscribe states an intent
// -- "stop sending me updates for this URI" -- and that intent is satisfied
// whether or not a subscription existed, so failing would report a problem the
// client does not have and force it to track server-side state just to avoid
// an error on a double release. The URI still has to name a registration,
// though: -32002 here means the client is talking about a resource this server
// does not offer at all, which is a fact worth surfacing before it subscribes
// to something that will never update.
func (s *Server) unsubscribeResource(params json.RawMessage) (json.RawMessage, *rpcError) {
	if !s.resourceSubscriptions {
		return nil, s.subscriptionNotAdvertised("resources/unsubscribe")
	}
	uri, rpcErr := resourceSubscriptionURI("resources/unsubscribe", params)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if _, ok := s.matchResource(uri); !ok {
		return nil, &rpcError{Code: codeResourceNotFound, Message: fmt.Sprintf("unknown resource %q", uri)}
	}
	s.requestMu.Lock()
	delete(s.subscriptions, uri)
	s.requestMu.Unlock()
	return mustMarshal(map[string]any{}), nil
}

// subscriptionCount is the number of URIs the current connection subscribed
// to. It exists for the same reason pendingRequestCount does: it makes "no
// per-connection state survived Serve" assertable from a test, which is
// otherwise invisible from outside the package.
func (s *Server) subscriptionCount() int {
	if s == nil {
		return 0
	}
	s.requestMu.Lock()
	defer s.requestMu.Unlock()
	return len(s.subscriptions)
}

// readResource serves resources/read. A URI no registered resource matches is
// the spec's resource-not-found error (-32002), stated in a comment at the
// constant: the client can tell "no such resource here" from "your request
// was malformed" (-32602) and from "the read failed" (-32603).
func (s *Server) readResource(ctx context.Context, params json.RawMessage) (json.RawMessage, *rpcError) {
	var request struct {
		URI string `json:"uri"`
	}
	if err := decodeMessage(params, &request); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "invalid resources/read params: " + err.Error()}
	}
	uri := strings.TrimSpace(request.URI)
	if uri == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: "resources/read needs a uri"}
	}
	serverResource, ok := s.matchResource(uri)
	if !ok {
		// -32002: this server has no resource for that URI. Refusing here is
		// the point of registering resources at all; calling a handler with an
		// arbitrary URI would turn a typo into whatever the handler does with
		// unknown input.
		return nil, &rpcError{Code: codeResourceNotFound, Message: fmt.Sprintf("unknown resource %q", uri)}
	}
	contents, err := s.invokeResource(ctx, serverResource, uri)
	if err != nil {
		if errors.Is(err, ErrResourceNotFound) {
			return nil, &rpcError{Code: codeResourceNotFound, Message: err.Error()}
		}
		return nil, &rpcError{Code: codeInternalError, Message: fmt.Sprintf("reading resource %q failed: %v", uri, err)}
	}
	if contents == nil {
		// The spec makes contents an array; a handler that read nothing should
		// answer with an empty one, not null.
		contents = []ResourceContent{}
	}
	return mustMarshal(map[string]any{"contents": contents}), nil
}

// invokeResource runs a handler, converting a panic into an error. It mirrors
// invoke for tools: a panicking resource must fail its call and leave the
// stream alive, because the peer would otherwise see a closed pipe instead of
// an answer and the reason would be lost.
func (s *Server) invokeResource(ctx context.Context, serverResource ServerResource, uri string) (contents []ResourceContent, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("mcp resource %q panicked: %v", serverResource.URI, recovered)
		}
	}()
	return serverResource.Handler(ctx, uri)
}
