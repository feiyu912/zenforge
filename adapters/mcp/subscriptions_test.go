package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// subscriptionServer builds a server with one plain resource, one template
// resource and one more plain resource, plus the subscription opt-in the test
// asks for. The two plain resources matter: one is deliverable as an exact
// subscription, and the other exists so a test can prove that a registered URI
// nobody subscribed to produces no notification.
func subscriptionServer(t *testing.T, subscriptions bool) *Server {
	t.Helper()
	server, err := NewServer(ServerConfig{
		Name:                  "zenforge",
		Version:               "test",
		ResourceSubscriptions: subscriptions,
		Tools: []ServerTool{{
			Name: "noop",
			Handler: func(ctx context.Context, arguments json.RawMessage) (CallResult, error) {
				return CallResult{}, nil
			},
		}},
		Resources: []ServerResource{
			{
				URI:         "zenforge://runs",
				Name:        "Recorded runs",
				Description: "An index",
				MimeType:    "application/json",
				Handler: func(ctx context.Context, uri string) ([]ResourceContent, error) {
					return []ResourceContent{{URI: uri, Text: "index"}}, nil
				},
			},
			{
				URI:         "zenforge://runs/{runId}",
				Name:        "Recorded run",
				Description: "One run",
				MimeType:    "application/json",
				Handler: func(ctx context.Context, uri string) ([]ResourceContent, error) {
					return []ResourceContent{{URI: uri, Text: "run"}}, nil
				},
			},
			{
				URI:  "zenforge://plain",
				Name: "Plain resource",
				Handler: func(ctx context.Context, uri string) ([]ResourceContent, error) {
					return nil, nil
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	return server
}

// subscribeResourceURI sends one resources/subscribe request and returns its
// result, failing the test if the server refused it.
func subscribeResourceURI(t *testing.T, stream *serverStream, id int, uri string) {
	t.Helper()
	stream.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"resources/subscribe","params":{"uri":%q}}`, id, uri))
	resultOf(t, stream.read())
}

// unsubscribeResourceURI sends one resources/unsubscribe request and returns
// its result, failing the test if the server refused it.
func unsubscribeResourceURI(t *testing.T, stream *serverStream, id int, uri string) {
	t.Helper()
	stream.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"resources/unsubscribe","params":{"uri":%q}}`, id, uri))
	resultOf(t, stream.read())
}

// TestServerListsResourceTemplatesAndExcludesPlainResources pins the discovery
// split: resources/templates/list carries only the registrations that are
// templates, under the spec's uriTemplate key, and never a resource that only
// matches itself. The optional cursor is accepted and ignored, because the
// server does not paginate and says so by sending no nextCursor.
func TestServerListsResourceTemplatesAndExcludesPlainResources(t *testing.T) {
	server := subscriptionServer(t, false)
	result := resultOf(t, call(t, server, `{"jsonrpc":"2.0","id":1,"method":"resources/templates/list"}`))
	templates, ok := result["resourceTemplates"].([]any)
	if !ok || len(templates) != 1 {
		t.Fatalf("resourceTemplates = %v", result["resourceTemplates"])
	}
	first := templates[0].(map[string]any)
	if first["uriTemplate"] != "zenforge://runs/{runId}" || first["name"] != "Recorded run" ||
		first["description"] != "One run" || first["mimeType"] != "application/json" {
		t.Fatalf("the first template is %v", first)
	}
	// A template is not a resource: it must not be listed under the resource
	// key, and the plain registrations must not appear at all.
	if _, ok := first["uri"]; ok {
		t.Fatalf("a template was listed with a resource's uri key: %v", first)
	}
	if _, ok := result["nextCursor"]; ok {
		t.Fatalf("pagination is not implemented, so no cursor may be sent: %v", result)
	}
	for _, entry := range templates {
		template := entry.(map[string]any)["uriTemplate"]
		if template == "zenforge://runs" || template == "zenforge://plain" {
			t.Fatalf("a resource with no template segment was listed as a template: %v", entry)
		}
	}
	// resources/list is unchanged: all three registrations are still there.
	resources := resultOf(t, call(t, server, `{"jsonrpc":"2.0","id":2,"method":"resources/list"}`))["resources"].([]any)
	if len(resources) != 3 {
		t.Fatalf("resources/list = %v, want all three registrations", resources)
	}
	// A cursor is tolerated and the full list comes back: this method has
	// nothing to paginate, so refusing the field would be gratuitous.
	result = resultOf(t, call(t, server, `{"jsonrpc":"2.0","id":3,"method":"resources/templates/list","params":{"cursor":"page-2"}}`))
	if templates, ok := result["resourceTemplates"].([]any); !ok || len(templates) != 1 {
		t.Fatalf("a cursor request did not return the full list: %v", result)
	}
}

// TestServerNotifiesSubscribedResourceUpdates pins the ordinary delivery: a
// client subscribes to an exact registered URI, NotifyResourceUpdated writes
// one notifications/resources/updated frame carrying that URI, and a
// registered URI nobody subscribed to writes nothing and is not an error.
func TestServerNotifiesSubscribedResourceUpdates(t *testing.T) {
	server := subscriptionServer(t, true)
	stream := startServerStream(t, server)
	subscribeResourceURI(t, stream, 1, "zenforge://runs")

	notified := make(chan error, 1)
	go func() { notified <- server.NotifyResourceUpdated("zenforge://runs") }()
	frame := stream.read()
	if frame["jsonrpc"] != "2.0" || frame["method"] != methodResourceUpdated {
		t.Fatalf("the update notification = %v", frame)
	}
	params, ok := frame["params"].(map[string]any)
	if !ok || params["uri"] != "zenforge://runs" {
		t.Fatalf("the update params = %v", frame["params"])
	}
	if err := <-notified; err != nil {
		t.Fatalf("NotifyResourceUpdated returned error: %v", err)
	}

	// Nobody subscribed to this one: an update with no listeners is normal, so
	// it is silent and succeeds.
	if err := server.NotifyResourceUpdated("zenforge://plain"); err != nil {
		t.Fatalf("NotifyResourceUpdated with no subscribers = %v", err)
	}
	// The ping proves the stream is intact and that no stray notification was
	// left in front of the response.
	stream.send(`{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	if pong := stream.read(); pong["id"] != float64(2) {
		t.Fatalf("an unnoticed update reached the stream: %v", pong)
	}
	stream.closeInput()
	if err := stream.wait(); err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
}

// TestServerNotifiesConcreteURIMatchingASubscribedTemplate pins the template
// half of update matching, in both directions a client can express it: it may
// subscribe to the template registration, meaning every instance, or to one
// concrete instance the template serves, meaning just that one.
func TestServerNotifiesConcreteURIMatchingASubscribedTemplate(t *testing.T) {
	t.Run("subscribed to the template", func(t *testing.T) {
		server := subscriptionServer(t, true)
		stream := startServerStream(t, server)
		subscribeResourceURI(t, stream, 1, "zenforge://runs/{runId}")

		notified := make(chan error, 1)
		go func() { notified <- server.NotifyResourceUpdated("zenforge://runs/run_7") }()
		frame := stream.read()
		if frame["method"] != methodResourceUpdated {
			t.Fatalf("the update notification = %v", frame)
		}
		if params, _ := frame["params"].(map[string]any); params["uri"] != "zenforge://runs/run_7" {
			t.Fatalf("the update params = %v", frame["params"])
		}
		if err := <-notified; err != nil {
			t.Fatalf("NotifyResourceUpdated returned error: %v", err)
		}
		stream.closeInput()
		if err := stream.wait(); err != nil {
			t.Fatalf("Serve returned error: %v", err)
		}
	})

	t.Run("subscribed to one concrete instance", func(t *testing.T) {
		server := subscriptionServer(t, true)
		stream := startServerStream(t, server)
		// The concrete URI is not registered as itself; the template serves it,
		// and subscribe must accept it for the same reason resources/read does.
		subscribeResourceURI(t, stream, 1, "zenforge://runs/run_1")

		notified := make(chan error, 1)
		go func() { notified <- server.NotifyResourceUpdated("zenforge://runs/run_1") }()
		frame := stream.read()
		if frame["method"] != methodResourceUpdated {
			t.Fatalf("the update notification = %v", frame)
		}
		if params, _ := frame["params"].(map[string]any); params["uri"] != "zenforge://runs/run_1" {
			t.Fatalf("the update params = %v", frame["params"])
		}
		if err := <-notified; err != nil {
			t.Fatalf("NotifyResourceUpdated returned error: %v", err)
		}
		// A sibling instance serves the same template but was not subscribed,
		// so it must not be delivered: one run's subscription is not every
		// run's.
		if err := server.NotifyResourceUpdated("zenforge://runs/run_2"); err != nil {
			t.Fatalf("NotifyResourceUpdated for an unsubscribed sibling = %v", err)
		}
		stream.send(`{"jsonrpc":"2.0","id":2,"method":"ping"}`)
		if pong := stream.read(); pong["id"] != float64(2) {
			t.Fatalf("a sibling run's update reached a run_1 subscriber: %v", pong)
		}
		stream.closeInput()
		if err := stream.wait(); err != nil {
			t.Fatalf("Serve returned error: %v", err)
		}
	})
}

// TestServerReportsSubscriptionFailuresDistinctly pins the two codes: a URI no
// registration serves is the resource-not-found code, while a request that
// never named a usable URI is the client's malformed request.
func TestServerReportsSubscriptionFailuresDistinctly(t *testing.T) {
	server := subscriptionServer(t, true)
	cases := []struct {
		name    string
		message string
		code    float64
	}{
		{"subscribe unknown uri", `{"jsonrpc":"2.0","id":1,"method":"resources/subscribe","params":{"uri":"zenforge://missing"}}`, codeResourceNotFound},
		{"unsubscribe unknown uri", `{"jsonrpc":"2.0","id":2,"method":"resources/unsubscribe","params":{"uri":"zenforge://missing"}}`, codeResourceNotFound},
		{"subscribe blank uri", `{"jsonrpc":"2.0","id":3,"method":"resources/subscribe","params":{"uri":"   "}}`, codeInvalidParams},
		{"unsubscribe missing uri", `{"jsonrpc":"2.0","id":4,"method":"resources/unsubscribe","params":{}}`, codeInvalidParams},
		{"subscribe non-string uri", `{"jsonrpc":"2.0","id":5,"method":"resources/subscribe","params":{"uri":7}}`, codeInvalidParams},
		{"unsubscribe bad params", `{"jsonrpc":"2.0","id":6,"method":"resources/unsubscribe","params":"not-an-object"}`, codeInvalidParams},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			errValue := errorOf(t, call(t, server, testCase.message))
			if errValue["code"] != testCase.code {
				t.Fatalf("code = %v, want %v (%v)", errValue["code"], testCase.code, errValue)
			}
		})
	}
}

// TestServerUnsubscribeIsIdempotent pins the documented choice that removing a
// subscription twice is not a failure: the intent is already satisfied, so the
// answer is a normal result and the subscription set ends up empty either way.
func TestServerUnsubscribeIsIdempotent(t *testing.T) {
	server := subscriptionServer(t, true)
	stream := startServerStream(t, server)
	subscribeResourceURI(t, stream, 1, "zenforge://runs")
	// Never subscribed, removed once, removed again: all three are results.
	unsubscribeResourceURI(t, stream, 2, "zenforge://plain")
	unsubscribeResourceURI(t, stream, 3, "zenforge://runs")
	unsubscribeResourceURI(t, stream, 4, "zenforge://runs")
	if got := server.subscriptionCount(); got != 0 {
		t.Fatalf("unsubscribe left %d subscriptions", got)
	}
	// The double removal really did remove it: no update is delivered now.
	if err := server.NotifyResourceUpdated("zenforge://runs"); err != nil {
		t.Fatalf("NotifyResourceUpdated after unsubscribe = %v", err)
	}
	stream.send(`{"jsonrpc":"2.0","id":5,"method":"ping"}`)
	if pong := stream.read(); pong["id"] != float64(5) {
		t.Fatalf("an unsubscribed client still received an update: %v", pong)
	}
	stream.closeInput()
	if err := stream.wait(); err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
}

// TestServerWithoutResourceSubscriptionsKeepsSubscribeFalse pins the default:
// the capability stays false, and both methods plus the Notify method fail
// loudly with a JSON-RPC error rather than silence, because the method was
// never part of what the server advertised.
func TestServerWithoutResourceSubscriptionsKeepsSubscribeFalse(t *testing.T) {
	server := subscriptionServer(t, false)
	capabilities := capabilitiesOf(t, server)
	resources, ok := capabilities["resources"].(map[string]any)
	if !ok || resources["subscribe"] != false || resources["listChanged"] != false {
		t.Fatalf("resources capability = %v", capabilities["resources"])
	}
	for _, method := range []string{"resources/subscribe", "resources/unsubscribe"} {
		t.Run(method, func(t *testing.T) {
			message := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":%q,"params":{"uri":"zenforge://runs"}}`, method)
			errValue := errorOf(t, call(t, server, message))
			if errValue["code"] != float64(codeMethodNotFound) {
				t.Fatalf("%s code = %v, want method-not-found (%v)", method, errValue["code"], errValue)
			}
			if text, _ := errValue["message"].(string); !strings.Contains(text, "ResourceSubscriptions") {
				t.Fatalf("%s error does not name the opt-in: %v", method, errValue)
			}
		})
	}
	if err := server.NotifyResourceUpdated("zenforge://runs"); err == nil || !strings.Contains(err.Error(), "ResourceSubscriptions") {
		t.Fatalf("NotifyResourceUpdated without the opt-in = %v", err)
	}
}

// TestServerWithResourceSubscriptionsAdvertisesSubscribeWithoutChangingListChanged
// pins the opt-in and its independence from DynamicLists in one place: turning
// subscriptions on makes subscribe true and leaves listChanged mirroring
// DynamicLists exactly.
func TestServerWithResourceSubscriptionsAdvertisesSubscribeWithoutChangingListChanged(t *testing.T) {
	cases := []struct {
		name    string
		dynamic bool
	}{
		{"fixed lists", false},
		{"dynamic lists", true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server, err := NewServer(ServerConfig{
				Name:                  "zenforge",
				Version:               "test",
				DynamicLists:          testCase.dynamic,
				ResourceSubscriptions: true,
				Tools: []ServerTool{{
					Name: "noop",
					Handler: func(ctx context.Context, arguments json.RawMessage) (CallResult, error) {
						return CallResult{}, nil
					},
				}},
				Resources: []ServerResource{{
					URI: "zenforge://runs",
					Handler: func(ctx context.Context, uri string) ([]ResourceContent, error) {
						return nil, nil
					},
				}},
			})
			if err != nil {
				t.Fatalf("NewServer returned error: %v", err)
			}
			capabilities := capabilitiesOf(t, server)
			resources, ok := capabilities["resources"].(map[string]any)
			if !ok || resources["subscribe"] != true {
				t.Fatalf("resources capability = %v, want subscribe true", capabilities["resources"])
			}
			if resources["listChanged"] != testCase.dynamic {
				t.Fatalf("listChanged = %v, want %v: subscribe and listChanged are independent",
					resources["listChanged"], testCase.dynamic)
			}
		})
	}
}

// TestServerSubscriptionsDoNotSurviveServe pins the lifetime rule: a
// subscription belongs to the connection, so Serve returning drops it and a
// second Serve on the same *Server starts with none. If the first
// connection's subscription leaked, the update below would be written to the
// new stream and the ping would read the notification instead of the pong.
func TestServerSubscriptionsDoNotSurviveServe(t *testing.T) {
	server := subscriptionServer(t, true)
	first := startServerStream(t, server)
	subscribeResourceURI(t, first, 1, "zenforge://runs")
	if got := server.subscriptionCount(); got != 1 {
		t.Fatalf("subscription count after subscribe = %d, want 1", got)
	}
	first.closeInput()
	if err := first.wait(); err != nil {
		t.Fatalf("the first Serve returned error: %v", err)
	}
	if got := server.subscriptionCount(); got != 0 {
		t.Fatalf("Serve left %d subscriptions behind", got)
	}

	second := startServerStream(t, server)
	// startServerStream returns as soon as Serve is launched, so wait for the
	// new connection to be attached before asking about it.
	waitUntil(t, 5*time.Second, server.isServing)
	if err := server.NotifyResourceUpdated("zenforge://runs"); err != nil {
		t.Fatalf("NotifyResourceUpdated on a fresh Serve = %v", err)
	}
	second.send(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if pong := second.read(); pong["id"] != float64(1) {
		t.Fatalf("the first connection's subscription leaked into the second: %v", pong)
	}
	second.closeInput()
	if err := second.wait(); err != nil {
		t.Fatalf("the second Serve returned error: %v", err)
	}
}

// TestServerNotifyResourceUpdatedRejectsUnknownAndBlankURIs pins the chosen
// handling of a caller mistake: announcing a URI this server never registered
// is an error rather than a silent drop, because the client would be told to
// re-read a resource it was never offered and would get resource-not-found.
func TestServerNotifyResourceUpdatedRejectsUnknownAndBlankURIs(t *testing.T) {
	server := subscriptionServer(t, true)
	if err := server.NotifyResourceUpdated("   "); err == nil {
		t.Fatal("a blank uri was announced")
	}
	err := server.NotifyResourceUpdated("zenforge://missing")
	if err == nil || !strings.Contains(err.Error(), "zenforge://missing") {
		t.Fatalf("an unregistered uri was announced: %v", err)
	}
	if errors.Is(err, ErrNotServing) {
		t.Fatalf("the refusal blamed the stream instead of the uri: %v", err)
	}
}

// streamBuffer is a writer that never blocks, so the shutdown race test can
// let deliveries and Serve's detach contend for writeMu without the writer
// itself serializing them into a deadlock. The server serializes whole frames
// under writeMu, so its content is still exactly one JSON document per line.
type streamBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *streamBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(data)
}

func (b *streamBuffer) lines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var lines []string
	for _, line := range strings.Split(b.buf.String(), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// TestServerNotifyResourceUpdatedRacingShutdownReturnsErrorsWithoutCorruptingTheStream
// pins both halves of the shutdown contract. Deliveries racing shutdown may
// win (nil) or lose (ErrNotServing) -- never anything else -- and once Serve
// has returned a delivery is refused with the same ErrNotServing the other
// Notify methods give. The writer records every frame, and the assertion that
// each line is one well-formed document is what "does not corrupt the stream"
// means.
func TestServerNotifyResourceUpdatedRacingShutdownReturnsErrorsWithoutCorruptingTheStream(t *testing.T) {
	server := subscriptionServer(t, true)
	input, clientInput := io.Pipe()
	var output streamBuffer
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background(), input, &output) }()

	if _, err := io.WriteString(clientInput, `{"jsonrpc":"2.0","id":1,"method":"resources/subscribe","params":{"uri":"zenforge://runs"}}`+"\n"); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	waitUntil(t, 5*time.Second, func() bool { return len(output.lines()) >= 1 })

	const racers = 16
	var wait sync.WaitGroup
	results := make(chan error, racers)
	for index := 0; index < racers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results <- server.NotifyResourceUpdated("zenforge://runs")
		}()
	}
	// End the connection while the deliveries are in flight.
	if err := clientInput.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}
	wait.Wait()
	close(results)
	for err := range results {
		if err != nil && !errors.Is(err, ErrNotServing) {
			t.Fatalf("a racing delivery returned %v, want nil or ErrNotServing", err)
		}
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return")
	}

	if err := server.NotifyResourceUpdated("zenforge://runs"); !errors.Is(err, ErrNotServing) {
		t.Fatalf("NotifyResourceUpdated after shutdown = %v, want ErrNotServing", err)
	}
	for _, line := range output.lines() {
		decoded := decodeFrame(t, []byte(line))
		switch decoded["method"] {
		case nil:
			if decoded["id"] != float64(1) {
				t.Fatalf("an unexpected response survived on the stream: %s", line)
			}
		case methodResourceUpdated:
			if params, _ := decoded["params"].(map[string]any); params["uri"] != "zenforge://runs" {
				t.Fatalf("a corrupt update notification: %s", line)
			}
		default:
			t.Fatalf("an unexpected frame survived on the stream: %s", line)
		}
	}
}
