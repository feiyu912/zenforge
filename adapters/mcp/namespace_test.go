package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestNamespacedNameUsesTheServerPrefix(t *testing.T) {
	if got := NamespacedName("github", "list_prs"); got != "mcp__github__list_prs" {
		t.Fatalf("NamespacedName = %q", got)
	}
	// A local tool can contain single underscores, so the double underscore is
	// what makes the namespace unambiguous.
	server, toolName, ok := SplitNamespacedName("mcp__github__list_prs")
	if !ok || server != "github" || toolName != "list_prs" {
		t.Fatalf("SplitNamespacedName = %q %q %v", server, toolName, ok)
	}
	if _, _, ok := SplitNamespacedName("read_file"); ok {
		t.Fatal("a local name was reported as namespaced")
	}
	if _, _, ok := SplitNamespacedName("mcp__github"); ok {
		t.Fatal("a name without a tool part was reported as namespaced")
	}
}

func TestNamespacedNameStaysInsideTheToolNameGrammar(t *testing.T) {
	// Characters outside [A-Za-z0-9_-] are replaced, because a client rejects
	// the name and with it the whole catalog.
	got := NamespacedName("My Server!", "read:file")
	if strings.ContainsAny(got, " !:") {
		t.Fatalf("NamespacedName kept characters the grammar forbids: %q", got)
	}
	if !strings.HasPrefix(got, "mcp__My_Server___") {
		t.Fatalf("NamespacedName = %q", got)
	}
	// A name that sanitizes to nothing still produces a usable name.
	if got := NamespacedName("***", "***"); got == "" || strings.Contains(got, "*") {
		t.Fatalf("NamespacedName = %q", got)
	}
}

func TestNamespacedNameStaysWithinTheLengthLimit(t *testing.T) {
	long := strings.Repeat("a", 200)
	got := NamespacedName("server", long)
	if len(got) > MaxToolNameLength {
		t.Fatalf("NamespacedName returned %d characters: %q", len(got), got)
	}
	// Truncation is deterministic, and two different long names do not
	// collapse onto the same one.
	if again := NamespacedName("server", long); again != got {
		t.Fatalf("NamespacedName is not deterministic: %q vs %q", got, again)
	}
	other := NamespacedName("server", long+"b")
	if other == got {
		t.Fatalf("two long names collided on %q", got)
	}
	if len(other) > MaxToolNameLength {
		t.Fatalf("the second name is %d characters", len(other))
	}
}

func TestToolExposesTheNamespacedNameButCallsTheRemoteOne(t *testing.T) {
	client := &fakeClient{
		definitions: []ToolDefinition{{
			Name:        "read_file",
			Description: "Read a file",
			Annotations: ToolAnnotations{ReadOnlyHint: boolPointer(true)},
		}},
	}
	built, err := ToolsWithOptions(context.Background(), client, ServerOptions{Server: "fs"})
	if err != nil {
		t.Fatalf("ToolsWithOptions returned error: %v", err)
	}
	if len(built) != 1 {
		t.Fatalf("built %d tools", len(built))
	}
	instance := built[0].(*Tool)
	if instance.Name() != "mcp__fs__read_file" {
		t.Fatalf("Name = %q", instance.Name())
	}
	if instance.RemoteName() != "read_file" || instance.ServerName() != "fs" {
		t.Fatalf("remote = %q, server = %q", instance.RemoteName(), instance.ServerName())
	}
	if !instance.ReadOnly() {
		t.Fatal("the readOnlyHint annotation was not read")
	}
	if _, err := instance.Call(context.Background(), json.RawMessage(`{"path":"a"}`), toolContext()); err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if client.calledName != "read_file" {
		t.Fatalf("the client was called with %q, want the remote name", client.calledName)
	}
	if len(client.calls) != 1 {
		t.Fatalf("the client was called %d times", len(client.calls))
	}
}

// TestToolsWithoutAServerKeepTheirNames keeps the short helper honest: a
// caller that gives no server name gets the remote names unchanged.
func TestToolsWithoutAServerKeepTheirNames(t *testing.T) {
	client := &fakeClient{definitions: []ToolDefinition{{Name: "read_file"}}}
	built, err := Tools(context.Background(), client)
	if err != nil {
		t.Fatalf("Tools returned error: %v", err)
	}
	if built[0].Name() != "read_file" {
		t.Fatalf("Name = %q", built[0].Name())
	}
	if tool, ok := built[0].(*Tool); !ok || tool.ReadOnly() {
		t.Fatal("an absent readOnlyHint must not report read-only")
	}
}

func TestToolsRefuseCollidingNamespacedNames(t *testing.T) {
	// Two remote names that sanitize to the same exposed name would silently
	// hide one tool, so construction fails instead.
	client := &fakeClient{definitions: []ToolDefinition{
		{Name: "read:file"},
		{Name: "read/file"},
	}}
	if _, err := ToolsWithOptions(context.Background(), client, ServerOptions{Server: "fs"}); err == nil {
		t.Fatal("colliding namespaced names were accepted")
	}
}

func TestToolDefinitionsReadAnnotations(t *testing.T) {
	var decoded struct {
		Tools []ToolDefinition `json:"tools"`
	}
	payload := `{"tools":[
		{"name":"read","annotations":{"readOnlyHint":true,"title":"Read"}},
		{"name":"write","annotations":{"readOnlyHint":false,"destructiveHint":true}},
		{"name":"unknown"}
	]}`
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	if !decoded.Tools[0].ReadOnly() || decoded.Tools[0].Annotations.Title != "Read" {
		t.Fatalf("the read-only tool decoded as %#v", decoded.Tools[0])
	}
	if decoded.Tools[1].ReadOnly() || !decoded.Tools[1].Destructive() {
		t.Fatalf("the destructive tool decoded as %#v", decoded.Tools[1])
	}
	// An absent hint is not read-only: guessing permissively is how a remote
	// tool that deletes files gets auto-approved.
	if decoded.Tools[2].ReadOnly() || decoded.Tools[2].Destructive() {
		t.Fatalf("a tool without hints decoded as %#v", decoded.Tools[2])
	}
	// The server's annotations round trip into the client's type, which is
	// how a `tools/list` answer becomes an approval decision.
	server := testServer(t)
	response, respond := server.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if !respond {
		t.Fatal("tools/list was not answered")
	}
	var listed struct {
		Result struct {
			Tools []ToolDefinition `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response, &listed); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	if !listed.Result.Tools[0].ReadOnly() {
		t.Fatalf("the server's readOnlyHint did not survive: %#v", listed.Result.Tools[0])
	}
}

func boolPointer(value bool) *bool { return &value }
