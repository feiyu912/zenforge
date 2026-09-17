package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/tool"
)

type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema,omitempty"`
	// Annotations are the hints the spec defines for a tool. readOnlyHint is
	// the one that matters most here: it is the difference between a call that
	// can be auto-approved and one that has to ask.
	Annotations ToolAnnotations `json:"annotations,omitempty"`
}

// ToolAnnotations mirrors the MCP tool annotations this client uses.
type ToolAnnotations struct {
	// ReadOnlyHint means the tool does not modify its environment.
	ReadOnlyHint *bool `json:"readOnlyHint,omitempty"`
	// DestructiveHint is kept so a policy can treat it as a stronger signal
	// than "not read-only": an absent hint is not the same as "harmless".
	DestructiveHint *bool `json:"destructiveHint,omitempty"`
	// OpenWorldHint means the tool interacts with an open world (anything
	// outside the client's context), which the reference treats as a reason
	// to ask even when the tool is not destructive.
	OpenWorldHint *bool `json:"openWorldHint,omitempty"`
	// Title is a human-readable label, used in approval prompts.
	Title string `json:"title,omitempty"`
}

// ReadOnly reports whether the server declared the tool as read-only. An
// absent hint is not read-only: the spec makes the hints optional, and
// guessing in the permissive direction is how a remote tool that deletes
// files gets auto-approved.
func (d ToolDefinition) ReadOnly() bool {
	return d.Annotations.ReadOnlyHint != nil && *d.Annotations.ReadOnlyHint
}

// Destructive reports whether the server declared the tool as destructive.
func (d ToolDefinition) Destructive() bool {
	return d.Annotations.DestructiveHint != nil && *d.Annotations.DestructiveHint
}

// OpenWorld reports whether the server declared the tool as reaching beyond
// the client's context.
func (d ToolDefinition) OpenWorld() bool {
	return d.Annotations.OpenWorldHint != nil && *d.Annotations.OpenWorldHint
}

// RequiresApproval reports whether a call to this tool has to pass the
// approval gate. It ports the reference's rule exactly:
//
//   - a destructive tool always asks;
//   - a read-only tool never asks;
//   - otherwise the tool asks unless the server explicitly declared it
//     non-destructive *and* closed-world.
//
// The last clause is the part that is easy to get wrong: "no read-only hint"
// is not "unknown, so probably safe", and the reference resolves it in the
// asking direction for an absent hint. A server that means to be trusted
// says so in both hints.
func (d ToolDefinition) RequiresApproval() bool {
	if d.Destructive() {
		return true
	}
	if d.ReadOnly() {
		return false
	}
	return d.Annotations.DestructiveHint == nil || *d.Annotations.DestructiveHint ||
		d.Annotations.OpenWorldHint == nil || *d.Annotations.OpenWorldHint
}

type CallResult struct {
	Content           []Content      `json:"content,omitempty"`
	StructuredContent map[string]any `json:"structuredContent,omitempty"`
	IsError           bool           `json:"isError,omitempty"`
}

type Content struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

type Tool struct {
	client     Client
	definition ToolDefinition
	deferred   bool
	// server is the MCP server this tool came from, when it was built with
	// one. It is what the model's tool name is namespaced with, and what the
	// approval layer uses to tell two servers' "read_file" apart.
	server string
	// approvalGate gates a call behind the approval broker unless the server
	// declared the tool safe to run unattended (see RequiresApproval).
	approvalGate bool
	// timeout is the declared cooperative budget for one remote call; zero
	// declares none.
	timeout time.Duration
}

// DeferredLoading marks remote MCP definitions for lazy activation
// through tool_search; see ToolsDeferred.
func (t *Tool) DeferredLoading() bool { return t.deferred }

// TimeoutBudget is the per-call deadline this server was configured with,
// mirroring the reference's per-server `toolCallTimeoutMs`. The client
// dispatches responses by id, so an expired call is abandoned without
// wedging the connection: the late response is dropped and the next call
// still works.
func (t *Tool) TimeoutBudget() time.Duration { return t.timeout }

// NamespacePrefix is what every remote tool name starts with. The double
// underscore is deliberate: a single one is a legal character in a tool name,
// so `mcp_server_tool` could collide with a local tool called exactly that,
// while `mcp__server__tool` cannot be produced by a local tool's own name
// without already claiming the prefix.
const NamespacePrefix = "mcp__"

// MaxToolNameLength is the longest tool name MCP clients accept. A name
// longer than this is rejected by the client, so a long server-plus-tool
// combination has to be shortened rather than sent.
const MaxToolNameLength = 64

// ServerOptions configures how a remote server's tools are exposed.
type ServerOptions struct {
	// Server names the server. When set, each tool's model-visible name is
	// namespaced (`mcp__<server>__<tool>`), so two servers can both offer
	// `read_file` without one shadowing the other.
	Server string
	// Deferred marks every definition for lazy activation through
	// tool_search, so a large remote catalog stays out of the model's
	// initial tool list until the model asks for it.
	Deferred bool
	// SkipApproval drops the approval gate for every tool of this server.
	// The zero value keeps the gate: a server's tools ask unless the server
	// declared them read-only, because a call that crosses a process and a
	// network boundary is a side effect until the server says otherwise.
	// It exists for a caller that has already made the trust decision
	// outside this package (an in-process bridge, or a test).
	SkipApproval bool
	// ToolCallTimeout is the declared cooperative budget for one remote
	// call, mirroring the reference's per-server tool-call timeout. Zero
	// declares none, which leaves the call unbounded.
	ToolCallTimeout time.Duration
}

// Tools exposes a client's tools without namespacing. It is the short form of
// ToolsWithOptions for a caller that only ever attaches one server.
func Tools(ctx context.Context, client Client) ([]tool.Tool, error) {
	return ToolsWithOptions(ctx, client, ServerOptions{})
}

// ToolsDeferred is Tools with every definition marked deferred.
func ToolsDeferred(ctx context.Context, client Client) ([]tool.Tool, error) {
	return ToolsWithOptions(ctx, client, ServerOptions{Deferred: true})
}

// ToolsWithOptions lists a server's tools and adapts them.
func ToolsWithOptions(ctx context.Context, client Client, options ServerOptions) ([]tool.Tool, error) {
	if client == nil {
		return nil, fmt.Errorf("mcp client is required")
	}
	server := strings.TrimSpace(options.Server)
	definitions, err := client.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]tool.Tool, 0, len(definitions))
	seen := make(map[string]string, len(definitions))
	for _, definition := range definitions {
		name := strings.TrimSpace(definition.Name)
		if name == "" {
			return nil, fmt.Errorf("mcp tool name is required")
		}
		definition.Name = name
		instance := &Tool{
			client:       client,
			definition:   definition,
			deferred:     options.Deferred,
			server:       server,
			approvalGate: !options.SkipApproval,
			timeout:      options.ToolCallTimeout,
		}
		// Two remote names can collapse onto one namespaced name (a 64-byte
		// truncation, or a name sanitized to the same string). Letting that
		// through would silently give the model one tool and hide the other,
		// so it is refused at construction.
		exposed := instance.Name()
		if previous, ok := seen[exposed]; ok {
			return nil, fmt.Errorf("mcp tools %q and %q both map to %q", previous, name, exposed)
		}
		seen[exposed] = name
		out = append(out, instance)
	}
	return out, nil
}

// Name is the name the model sees: the namespaced one when a server was
// given, otherwise the remote name.
func (t *Tool) Name() string {
	if t.server == "" {
		return t.definition.Name
	}
	return NamespacedName(t.server, t.definition.Name)
}

// RemoteName is the name the remote server knows the tool by. A call must use
// this one: the namespace exists for the model and the policy layer, not for
// the server.
func (t *Tool) RemoteName() string {
	return t.definition.Name
}

// ServerName is the server this tool came from, if one was given.
func (t *Tool) ServerName() string {
	return t.server
}

// ReadOnly reports whether the server declared the tool read-only.
func (t *Tool) ReadOnly() bool {
	return t.definition.ReadOnly()
}

// namespacedName builds the model-visible name for a server's tool.
//
// The result always matches the MCP tool-name grammar (`[A-Za-z0-9_-]`, at
// most 64 characters): a name the client rejects is worse than a shortened
// one, because the whole server's catalog fails. Characters outside the
// grammar are replaced with `_`, and an over-long name is truncated with a
// short hash of the full name appended, so two different long names do not
// silently become the same name.
func NamespacedName(server, toolName string) string {
	server = sanitizeToolName(server)
	toolName = sanitizeToolName(toolName)
	full := NamespacePrefix + server + "__" + toolName
	if len(full) <= MaxToolNameLength {
		return full
	}
	suffix := "~" + shortHash(full)
	keep := MaxToolNameLength - len(suffix)
	if keep < len(NamespacePrefix) {
		keep = len(NamespacePrefix)
	}
	return full[:keep] + suffix
}

// SplitNamespacedName recovers the server and remote tool name from a
// namespaced name. The boolean is false when the name was not namespaced, so
// a caller never has to guess whether a tool is remote.
func SplitNamespacedName(name string) (string, string, bool) {
	if !strings.HasPrefix(name, NamespacePrefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(name, NamespacePrefix)
	server, toolName, ok := strings.Cut(rest, "__")
	if !ok || server == "" || toolName == "" {
		return "", "", false
	}
	return server, toolName, true
}

// sanitizeToolName maps a name onto the MCP tool-name grammar.
func sanitizeToolName(name string) string {
	name = strings.TrimSpace(name)
	var builder strings.Builder
	builder.Grow(len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			builder.WriteRune(r)
		default:
			builder.WriteRune('_')
		}
	}
	if builder.Len() == 0 {
		return "server"
	}
	return builder.String()
}

// shortHash is a short, stable digest of a name, used only to keep two
// truncated names apart. It is not a security boundary.
func shortHash(value string) string {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(value))
	return fmt.Sprintf("%06x", hash.Sum32())
}

func (t *Tool) Description() string {
	return t.definition.Description
}

func (t *Tool) Schema() map[string]any {
	if t.definition.InputSchema == nil {
		return map[string]any{"type": "object"}
	}
	return cloneMap(t.definition.InputSchema)
}

func (t *Tool) Call(ctx context.Context, input json.RawMessage, call tool.Context) (tool.Result, error) {
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	if t.approvalGate && t.definition.RequiresApproval() {
		fingerprint, ruleKey := t.approvalIdentity(input)
		if !approval.MatchesApprovedMetadata(call.Metadata, fingerprint, ruleKey) {
			return approval.RequiredResult(t.approvalRequest(call, fingerprint, ruleKey)), approval.ErrRequired
		}
	}
	result, err := t.client.CallTool(ctx, t.RemoteName(), input)
	if err != nil {
		return tool.Result{Error: err.Error(), ExitCode: 1}, err
	}
	output := resultText(result)
	out := tool.Result{
		Output:     output,
		Structured: cloneMap(result.StructuredContent),
		Metadata: map[string]any{
			"mcp": map[string]any{
				"isError":  result.IsError,
				"content":  result.Content,
				"server":   t.server,
				"tool":     t.RemoteName(),
				"readOnly": t.ReadOnly(),
			},
		},
	}
	if result.IsError {
		out.Error = output
		out.ExitCode = 1
	}
	return out, nil
}

// approvalRequest builds the request that gates one remote call.
//
// The rule key is the tool's identity (`mcp:<server>:<tool>`), which is the
// scope the reference persists a session approval against: "always allow this
// server's tool", not "allow it once with these arguments". The fingerprint
// additionally covers the arguments, so a broker offering a run-scoped
// approval is offering exactly this call and nothing wider.
func (t *Tool) approvalRequest(call tool.Context, fingerprint, ruleKey string) approval.Request {
	description := strings.TrimSpace(t.definition.Description)
	if description == "" {
		description = "The server did not describe this tool."
	}
	risk := approval.RiskMedium
	if t.definition.Destructive() {
		risk = approval.RiskHigh
	}
	return approval.Request{
		ID:          approval.NewRequestID(call.RunID, call.ToolCallID, "mcp.tool"),
		RunID:       call.RunID,
		ToolCallID:  call.ToolCallID,
		ToolName:    t.Name(),
		Operation:   "mcp.tool",
		Title:       "Approve MCP tool " + t.Name(),
		Description: description,
		Risk:        risk,
		Options:     approval.DefaultOptions(),
		Payload: map[string]any{
			"server":      t.server,
			"tool":        t.RemoteName(),
			"readOnly":    t.ReadOnly(),
			"destructive": t.definition.Destructive(),
			"openWorld":   t.definition.OpenWorld(),
			"fingerprint": fingerprint,
			"ruleKey":     ruleKey,
		},
		CreatedAt: time.Now().UTC(),
	}
}

// approvalIdentity derives the two keys an approval decision is scoped by.
// The fingerprint hashes the tool identity together with the arguments, so a
// run-scoped approval cannot be replayed for a different call; the rule key
// names the tool alone.
func (t *Tool) approvalIdentity(input json.RawMessage) (fingerprint, ruleKey string) {
	ruleKey = "mcp:" + t.server + ":" + t.RemoteName()
	sum := sha256.Sum256(append([]byte(ruleKey+"\x00"), input...))
	return ruleKey + ":" + hex.EncodeToString(sum[:8]), ruleKey
}

func resultText(result CallResult) string {
	parts := make([]string, 0, len(result.Content))
	for _, item := range result.Content {
		switch item.Type {
		case "text":
			if item.Text != "" {
				parts = append(parts, item.Text)
			}
		default:
			if item.Text != "" {
				parts = append(parts, item.Text)
			} else if item.Data != "" {
				parts = append(parts, item.Data)
			}
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}
	if len(result.StructuredContent) == 0 {
		return ""
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return ""
	}
	return string(data)
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
