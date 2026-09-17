package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/tool"
)

func TestToolDefinitionRequiresApprovalFollowsTheAnnotations(t *testing.T) {
	yes := true
	no := false
	cases := []struct {
		name        string
		annotations ToolAnnotations
		require     bool
	}{
		{"no hints at all asks", ToolAnnotations{}, true},
		{"read-only is never gated", ToolAnnotations{ReadOnlyHint: &yes}, false},
		{"destructive is always gated", ToolAnnotations{ReadOnlyHint: &yes, DestructiveHint: &yes}, true},
		{"destructive and not read-only is gated", ToolAnnotations{DestructiveHint: &yes}, true},
		{"an absent destructive hint still asks", ToolAnnotations{OpenWorldHint: &no}, true},
		{"an absent open-world hint still asks", ToolAnnotations{DestructiveHint: &no}, true},
		{"explicitly harmless and closed-world is trusted", ToolAnnotations{DestructiveHint: &no, OpenWorldHint: &no}, false},
		{"open-world asks", ToolAnnotations{DestructiveHint: &no, OpenWorldHint: &yes}, true},
		{"an explicit false read-only hint is not read-only", ToolAnnotations{ReadOnlyHint: &no, DestructiveHint: &no, OpenWorldHint: &no}, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			definition := ToolDefinition{Name: "danger", Annotations: testCase.annotations}
			if got := definition.RequiresApproval(); got != testCase.require {
				t.Fatalf("RequiresApproval() = %v, want %v", got, testCase.require)
			}
		})
	}
}

func TestToolWithoutAHintAsksBeforeCallingTheServer(t *testing.T) {
	client := &fakeClient{
		definitions: []ToolDefinition{{Name: "delete", Description: "Delete a record."}},
		result:      CallResult{Content: []Content{{Type: "text", Text: "deleted"}}},
	}
	tools, err := ToolsWithOptions(context.Background(), client, ServerOptions{Server: "records"})
	if err != nil {
		t.Fatalf("ToolsWithOptions returned error: %v", err)
	}
	result, err := tools[0].Call(context.Background(), json.RawMessage(`{"id":"7"}`), toolContext())
	if !errors.Is(err, approval.ErrRequired) {
		t.Fatalf("Call error = %v, want approval.ErrRequired", err)
	}
	if result.Error != approval.ErrorRequired {
		t.Fatalf("result error = %q, want %q", result.Error, approval.ErrorRequired)
	}
	if len(client.calls) != 0 {
		t.Fatalf("the remote tool ran before approval: %v", client.calls)
	}
	req, ok := approval.RequestFromResult(result)
	if !ok {
		t.Fatalf("no approval request in the result: %#v", result)
	}
	if req.ToolName != "mcp__records__delete" || req.Operation != "mcp.tool" {
		t.Fatalf("unexpected request identity: %#v", req)
	}
	if req.Payload["server"] != "records" || req.Payload["tool"] != "delete" {
		t.Fatalf("request payload lost the tool identity: %#v", req.Payload)
	}
	if req.Payload["readOnly"] != false {
		t.Fatalf("readOnly payload = %#v", req.Payload["readOnly"])
	}
	fingerprint, _ := req.Payload["fingerprint"].(string)
	ruleKey, _ := req.Payload["ruleKey"].(string)
	if fingerprint == "" || ruleKey != "mcp:records:delete" {
		t.Fatalf("approval keys = %q / %q", fingerprint, ruleKey)
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("request does not validate: %v", err)
	}
}

func TestApprovedMetadataRunsTheGatedCall(t *testing.T) {
	client := &fakeClient{
		definitions: []ToolDefinition{{Name: "delete"}},
		result:      CallResult{Content: []Content{{Type: "text", Text: "deleted"}}},
	}
	tools, err := ToolsWithOptions(context.Background(), client, ServerOptions{Server: "records"})
	if err != nil {
		t.Fatalf("ToolsWithOptions returned error: %v", err)
	}
	input := json.RawMessage(`{"id":"7"}`)
	required, err := tools[0].Call(context.Background(), input, toolContext())
	if !errors.Is(err, approval.ErrRequired) {
		t.Fatalf("first call error = %v", err)
	}
	req, _ := approval.RequestFromResult(required)
	decision := approval.Decision{
		RequestID: req.ID,
		Action:    approval.DecisionApprove,
		Scope:     approval.ScopeOnce,
		DecidedAt: time.Now().UTC(),
	}
	metadata := approval.ApprovedMetadata(nil, req, decision)
	result, err := tools[0].Call(context.Background(), input, tool.Context{Metadata: metadata})
	if err != nil {
		t.Fatalf("approved call error = %v", err)
	}
	if result.Output != "deleted" || len(client.calls) != 1 {
		t.Fatalf("approved call result = %#v, remote calls = %v", result, client.calls)
	}
}

func TestRuleScopedApprovalCoversDifferentArguments(t *testing.T) {
	client := &fakeClient{
		definitions: []ToolDefinition{{Name: "delete"}},
		result:      CallResult{Content: []Content{{Type: "text", Text: "deleted"}}},
	}
	tools, err := ToolsWithOptions(context.Background(), client, ServerOptions{Server: "records"})
	if err != nil {
		t.Fatalf("ToolsWithOptions returned error: %v", err)
	}
	required, err := tools[0].Call(context.Background(), json.RawMessage(`{"id":"7"}`), toolContext())
	if !errors.Is(err, approval.ErrRequired) {
		t.Fatalf("first call error = %v", err)
	}
	req, _ := approval.RequestFromResult(required)
	// "Always allow this tool" is a rule-scoped decision: the metadata still
	// carries the rule key, so a different argument set matches too.
	metadata := approval.ApprovedMetadata(nil, req, approval.Decision{
		RequestID: req.ID,
		Action:    approval.DecisionAlways,
		Scope:     approval.ScopeRule,
		DecidedAt: time.Now().UTC(),
	})
	if _, err := tools[0].Call(context.Background(), json.RawMessage(`{"id":"8"}`), tool.Context{Metadata: metadata}); err != nil {
		t.Fatalf("rule-scoped call error = %v", err)
	}
}

func TestFingerprintCoversTheArguments(t *testing.T) {
	client := &fakeClient{definitions: []ToolDefinition{{Name: "delete"}}}
	tools, err := ToolsWithOptions(context.Background(), client, ServerOptions{Server: "records"})
	if err != nil {
		t.Fatalf("ToolsWithOptions returned error: %v", err)
	}
	first, err := tools[0].Call(context.Background(), json.RawMessage(`{"id":"7"}`), toolContext())
	if !errors.Is(err, approval.ErrRequired) {
		t.Fatalf("first call error = %v", err)
	}
	second, err := tools[0].Call(context.Background(), json.RawMessage(`{"id":"8"}`), toolContext())
	if !errors.Is(err, approval.ErrRequired) {
		t.Fatalf("second call error = %v", err)
	}
	firstReq, _ := approval.RequestFromResult(first)
	secondReq, _ := approval.RequestFromResult(second)
	// A run-scoped grant is matched by this fingerprint against the retried
	// call's own request, so two different payloads must not share one.
	if firstReq.Payload["fingerprint"] == secondReq.Payload["fingerprint"] {
		t.Fatalf("two different calls share a fingerprint: %#v", firstReq.Payload["fingerprint"])
	}
	if firstReq.Payload["ruleKey"] != secondReq.Payload["ruleKey"] {
		t.Fatalf("the same tool produced different rule keys: %#v / %#v", firstReq.Payload["ruleKey"], secondReq.Payload["ruleKey"])
	}
}

func TestReadOnlyHintSkipsTheGate(t *testing.T) {
	yes := true
	client := &fakeClient{
		definitions: []ToolDefinition{{Name: "list", Annotations: ToolAnnotations{ReadOnlyHint: &yes}}},
		result:      CallResult{Content: []Content{{Type: "text", Text: "ok"}}},
	}
	tools, err := ToolsWithOptions(context.Background(), client, ServerOptions{Server: "records"})
	if err != nil {
		t.Fatalf("ToolsWithOptions returned error: %v", err)
	}
	result, err := tools[0].Call(context.Background(), nil, toolContext())
	if err != nil {
		t.Fatalf("read-only call error = %v", err)
	}
	if result.Output != "ok" {
		t.Fatalf("read-only output = %q", result.Output)
	}
	if payload, _ := result.Metadata["mcp"].(map[string]any); payload["readOnly"] != true {
		t.Fatalf("result metadata lost the read-only hint: %#v", result.Metadata)
	}
}

func TestSkipApprovalTrustsTheWholeServer(t *testing.T) {
	client := &fakeClient{
		definitions: []ToolDefinition{{Name: "delete"}},
		result:      CallResult{Content: []Content{{Type: "text", Text: "deleted"}}},
	}
	tools, err := ToolsWithOptions(context.Background(), client, ServerOptions{Server: "records", SkipApproval: true})
	if err != nil {
		t.Fatalf("ToolsWithOptions returned error: %v", err)
	}
	if _, err := tools[0].Call(context.Background(), nil, toolContext()); err != nil {
		t.Fatalf("trusted call error = %v", err)
	}
}

func TestServerOptionsDeclareTheToolCallTimeout(t *testing.T) {
	client := &fakeClient{definitions: []ToolDefinition{{Name: "slow"}}}
	tools, err := ToolsWithOptions(context.Background(), client, ServerOptions{Server: "records", ToolCallTimeout: 90 * time.Second})
	if err != nil {
		t.Fatalf("ToolsWithOptions returned error: %v", err)
	}
	if got := tool.TimeoutBudgetOf(tools[0]); got != 90*time.Second {
		t.Fatalf("TimeoutBudgetOf = %s", got)
	}
	plain, err := ToolsWithOptions(context.Background(), client, ServerOptions{Server: "records"})
	if err != nil {
		t.Fatalf("ToolsWithOptions returned error: %v", err)
	}
	if got := tool.TimeoutBudgetOf(plain[0]); got != 0 {
		t.Fatalf("undeclared budget = %s", got)
	}
}
