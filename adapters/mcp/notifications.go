package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

// Notification methods this server can send. Progress is tied to one request
// and is emitted by the handler that is answering it; the list-changed trio is
// server-initiated and only valid when initialize advertised listChanged for
// that surface; resource-updated is server-initiated and only valid for a URI
// some client subscribed to, which is only possible when initialize advertised
// resources.subscribe.
const (
	methodProgress             = "notifications/progress"
	methodToolsListChanged     = "notifications/tools/list_changed"
	methodResourcesListChanged = "notifications/resources/list_changed"
	methodPromptsListChanged   = "notifications/prompts/list_changed"
	methodResourceUpdated      = "notifications/resources/updated"
)

// NotificationSink receives one already-encoded notification frame, without a
// trailing newline. A caller that owns a transport installs one with
// [WithNotificationSink] so the handlers it drives through [Server.Handle] can
// emit progress on that caller's stream.
//
// An error means the frame could not be written. A notification has no
// response, so there is nobody to tell: the error is dropped and the write of
// the request's response is what reports a broken transport to its owner.
type NotificationSink func(frame []byte) error

type notificationSinkKey struct{}

// WithNotificationSink returns a context that routes the notifications a
// handler emits while answering the request to sink. It is the in-process
// counterpart of the stream [Server.Serve] owns: a bridge or test that drives
// [Server.Handle] itself supplies the writer this way, and a handler still
// reaches progress through the same [ProgressFrom] call it would use anywhere
// else.
//
// A nil sink returns ctx unchanged, so a caller that has no stream to write to
// simply gets no notifications rather than a panic.
func WithNotificationSink(ctx context.Context, sink NotificationSink) context.Context {
	if sink == nil {
		return ctx
	}
	return context.WithValue(ctx, notificationSinkKey{}, sink)
}

func notificationSinkFrom(ctx context.Context) NotificationSink {
	sink, _ := ctx.Value(notificationSinkKey{}).(NotificationSink)
	return sink
}

// progressReporter is the per-request progress channel installed when a
// request carried a token.
type progressReporter func(progress float64, message string)

type progressReporterKey struct{}

// ProgressFrom returns the reporter for the request being handled under ctx.
//
// A handler that might take a while calls it once and reports as it goes:
//
//	report := mcp.ProgressFrom(ctx)
//	report(1, "reading the store")
//	report(2, "summarizing")
//
// When the client did not ask for progress -- no params._meta.progressToken,
// or a transport with no sink -- the returned function is a no-op, so the
// handler never has to check whether progress is wanted. A nil context or a
// context with no request in flight gets the same no-op.
//
// Progress is written inline by the goroutine that is already handling the
// request, before that request's response. That is possible because a
// notification is one-way: it has no response to read, so unlike a
// server-initiated *request* it needs no second reader and no writer staged on
// another goroutine. It also means the handler must report from its own
// goroutine, while the call is still running -- reporting after returning
// would interleave with whatever the server writes next.
func ProgressFrom(ctx context.Context) func(progress float64, message string) {
	if ctx == nil {
		return func(float64, string) {}
	}
	reporter, ok := ctx.Value(progressReporterKey{}).(progressReporter)
	if !ok || reporter == nil {
		return func(float64, string) {}
	}
	return reporter
}

// withProgress installs [ProgressFrom] support when the request params carry
// params._meta.progressToken and the transport offered a sink. Every no-token
// request leaves ctx untouched, which is what keeps a client that did not ask
// for progress from receiving a single notification.
func withProgress(ctx context.Context, params json.RawMessage) context.Context {
	token := progressToken(params)
	if len(token) == 0 {
		return ctx
	}
	sink := notificationSinkFrom(ctx)
	if sink == nil {
		return ctx
	}
	reporter := progressReporter(func(progress float64, message string) {
		sendProgress(sink, token, progress, message)
	})
	return context.WithValue(ctx, progressReporterKey{}, reporter)
}

// progressToken reads params._meta.progressToken, keeping the raw JSON so the
// token goes back exactly as it arrived: the spec allows a string or a number,
// and a number that came back as a string (or rounded through a float) would
// not match the token the client is waiting on. An absent, null, or malformed
// token means the client did not ask for progress.
func progressToken(params json.RawMessage) json.RawMessage {
	if len(params) == 0 {
		return nil
	}
	var envelope struct {
		Meta json.RawMessage `json:"_meta"`
	}
	if err := decodeMessage(params, &envelope); err != nil || len(envelope.Meta) == 0 {
		return nil
	}
	var meta struct {
		ProgressToken json.RawMessage `json:"progressToken"`
	}
	if err := decodeMessage(envelope.Meta, &meta); err != nil {
		return nil
	}
	token := bytes.TrimSpace(meta.ProgressToken)
	if len(token) == 0 || string(token) == "null" {
		return nil
	}
	return token
}

// progressNotification is the wire form of notifications/progress. Message is
// omitted when empty, which is what the spec's "optional" means.
type progressNotification struct {
	JSONRPC string         `json:"jsonrpc"`
	Method  string         `json:"method"`
	Params  progressParams `json:"params"`
}

type progressParams struct {
	ProgressToken json.RawMessage `json:"progressToken"`
	Progress      float64         `json:"progress"`
	Message       string          `json:"message,omitempty"`
}

// sendProgress encodes and hands one progress notification to sink.
//
// JSON has no NaN or Infinity, and json.Marshal refuses them, so a handler
// that reports one gets no notification at all. Refusing beats clamping: a
// clamped number would tell the client a value the handler never meant, while
// dropping the report leaves the stream well formed and the client with
// whatever it already had. The check happens before anything is written, and
// the frame is encoded whole, so a bad report can never leave a partial line
// on the stream.
func sendProgress(sink NotificationSink, token json.RawMessage, progress float64, message string) {
	if math.IsNaN(progress) || math.IsInf(progress, 0) {
		return
	}
	frame, err := json.Marshal(progressNotification{
		JSONRPC: "2.0",
		Method:  methodProgress,
		Params: progressParams{
			ProgressToken: token,
			Progress:      progress,
			Message:       message,
		},
	})
	if err != nil {
		return
	}
	_ = sink(frame)
}

// NotifyToolsChanged tells the client the tool list changed. See
// notifyListChanged for when it is allowed and where the frame goes.
func (s *Server) NotifyToolsChanged() error {
	return s.notifyListChanged("tools", len(s.tools) > 0, methodToolsListChanged)
}

// NotifyResourcesChanged tells the client the resource list changed.
func (s *Server) NotifyResourcesChanged() error {
	return s.notifyListChanged("resources", len(s.resources) > 0, methodResourcesListChanged)
}

// NotifyPromptsChanged tells the client the prompt list changed.
func (s *Server) NotifyPromptsChanged() error {
	return s.notifyListChanged("prompts", len(s.prompts) > 0, methodPromptsListChanged)
}

// notifyListChanged writes one list_changed notification, or explains why it
// must not.
//
// The capability block is a promise the client plans around: a server that
// advertised listChanged: false told the client its lists never change, so
// sending one anyway would be a notification the client has every right to
// ignore -- and worse, it would make the next `tools/list` cache look like a
// client bug. The refusals are therefore errors rather than silent drops, so
// a server that meant to be dynamic learns at the call site that it never
// advertised it, and a Notify call for a surface the server does not serve
// fails instead of announcing a list the client was never told about.
func (s *Server) notifyListChanged(surface string, served bool, method string) error {
	if s == nil {
		return fmt.Errorf("mcp server is nil, so %s cannot be sent", method)
	}
	if !s.dynamicLists {
		return fmt.Errorf("mcp server %q advertises listChanged false for %s; set ServerConfig.DynamicLists to send %s", s.name, surface, method)
	}
	if !served {
		return fmt.Errorf("mcp server %q serves no %s, so %s would announce a list the client was never told about", s.name, surface, method)
	}
	frame, err := json.Marshal(struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
	}{JSONRPC: "2.0", Method: method})
	if err != nil {
		return err
	}
	return s.send(frame)
}

// NotifyResourceUpdated tells the connected client that the resource at uri
// changed, so a client that subscribed to it can re-read it. The notification
// carries the URI the caller gave, which is the concrete resource the client
// should refresh.
//
// A subscription is a URI the client named, and that URI may be the template
// registration itself (`zenforge://runs/{runId}`, meaning "every run") or a
// concrete instance that a template serves (`zenforge://runs/run_1`, meaning
// "this run"). The argument is matched against each subscription the same way
// resources/read matches a request: an exact string, or a registered template
// whose placeholder covers the concrete URI. That is what makes "the run that
// just finished" reach the client watching that run and "a run changed" reach
// the client watching the collection.
//
// The refusals are errors, matching the list-changed methods, so a server that
// meant to notify learns at the call site instead of sending a frame the
// client has no context for:
//
//   - without ServerConfig.ResourceSubscriptions the capability was never
//     advertised, so no client can be subscribed and the call is a misuse;
//   - a blank or unregistered URI would announce a resource the client was
//     never offered, and a client that trusted it would re-read and get
//     resource-not-found -- dropping it instead would hide the caller's bug,
//     so it is refused;
//   - with no stream there is nowhere to write, reported as ErrNotServing like
//     every other Notify method. A delivery that races shutdown therefore
//     returns exactly that error rather than blocking or writing into a
//     detached stream: the subscription set and serving are one fact under
//     requestMu, and the write itself goes through send's writeMu.
//
// An update with no subscribers is not a failure. A resource changing while
// nobody is watching is the normal case, so it returns nil and writes nothing.
func (s *Server) NotifyResourceUpdated(uri string) error {
	if s == nil {
		return errors.New("mcp server is nil, so notifications/resources/updated cannot be sent")
	}
	if !s.resourceSubscriptions {
		return fmt.Errorf("mcp server %q advertises resources.subscribe false; set ServerConfig.ResourceSubscriptions to send notifications/resources/updated", s.name)
	}
	target := strings.TrimSpace(uri)
	if target == "" {
		return errors.New("notifications/resources/updated needs a uri")
	}
	if _, ok := s.matchResource(target); !ok {
		return fmt.Errorf("mcp server %q has no resource matching %q, so notifications/resources/updated would announce a resource the client was never offered", s.name, target)
	}
	// The serving check and the subscription snapshot are taken together, so a
	// call that races Serve's shutdown sees either a live connection and its
	// subscribers or a closed one -- never a live flag with a set that
	// failPending has already emptied and a nil answer that would look like
	// "nobody was listening".
	s.requestMu.Lock()
	if !s.serving {
		s.requestMu.Unlock()
		return ErrNotServing
	}
	wanted := false
	for subscribed := range s.subscriptions {
		if subscribed == target || resourceMatches(subscribed, target) {
			wanted = true
			break
		}
	}
	s.requestMu.Unlock()
	if !wanted {
		return nil
	}
	frame, err := json.Marshal(struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  struct {
			URI string `json:"uri"`
		} `json:"params"`
	}{
		JSONRPC: "2.0",
		Method:  methodResourceUpdated,
		Params: struct {
			URI string `json:"uri"`
		}{URI: target},
	})
	if err != nil {
		return err
	}
	// send handles the shutdown race: it writes under writeMu, so it either
	// lands before detach or finds the stream gone and reports ErrNotServing.
	return s.send(frame)
}
