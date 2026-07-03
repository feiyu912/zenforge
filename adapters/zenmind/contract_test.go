package zenmind

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/approval"
)

func TestPlatformContractBuildProjectPersistResumeAndApprove(t *testing.T) {
	ctx := context.Background()
	agent := CatalogAgent{
		Key: "agent-contract", Instructions: "Use tools carefully.",
		Mode: "REACT", Tools: []string{},
	}
	session := Session{
		RequestID: "request-contract", ChatID: "chat-contract",
		RunID: "run-contract", AgentKey: "agent-contract", Message: "Inspect the contract.",
	}
	runtime := runtimeWithModel()
	router := Router{
		Default: RouteZenForge,
		Initialize: func(input RouteInput) error {
			_, err := BuildRun(ctx, agent, session, runtime)
			return err
		},
	}
	if got := router.Decide(agent, session); got != RouteZenForge {
		t.Fatalf("route = %q, want %q", got, RouteZenForge)
	}
	run, err := BuildRun(ctx, agent, session, runtime)
	if err != nil {
		t.Fatalf("BuildRun: %v", err)
	}
	zenmindMeta, _ := run.Task.Meta["zenmind"].(map[string]any)
	sessionMeta, _ := zenmindMeta["session"].(map[string]any)
	if run.Task.RunID != session.RunID || sessionMeta["chatId"] != session.ChatID ||
		sessionMeta["agentKey"] != session.AgentKey {
		t.Fatalf("run identity was not preserved: %#v", run.Task)
	}

	projector := NewProjectorWithIdentity(ProjectorIdentity{
		RunID: session.RunID, ChatID: session.ChatID, AgentKey: session.AgentKey,
	})
	var projected []StreamEvent
	project := func(event zenforge.Event) {
		t.Helper()
		events, err := projector.ProjectStrict(event)
		if err != nil {
			t.Fatalf("ProjectStrict(%s): %v", event.Type, err)
		}
		projected = append(projected, events...)
	}
	project(zenforge.NewEvent(zenforge.EventRunStarted, session.RunID, nil))
	project(zenforge.NewEvent(
		zenforge.EventModelDelta, session.RunID, map[string]any{"textDelta": "hello"}))

	stateJSON, err := json.Marshal(projector.Snapshot())
	if err != nil {
		t.Fatalf("Marshal projector state: %v", err)
	}
	var state ProjectorState
	if err := json.Unmarshal(stateJSON, &state); err != nil {
		t.Fatalf("Unmarshal projector state: %v", err)
	}
	projector, err = NewProjectorFromState(state)
	if err != nil {
		t.Fatalf("NewProjectorFromState: %v", err)
	}
	project(zenforge.NewEvent(
		zenforge.EventModelDelta, session.RunID, map[string]any{"textDelta": " world"}))
	project(zenforge.NewEvent(zenforge.EventModelDone, session.RunID, nil))
	project(zenforge.NewEvent(zenforge.EventRunDone, session.RunID, nil))

	root := t.TempDir()
	writer := NewChatJSONLWriter(root)
	for _, event := range projected {
		if err := writer.Append(ctx, session.ChatID, event); err != nil {
			t.Fatalf("Append projected event %#v: %v", event, err)
		}
	}
	lines, err := ReadEventLines(ctx, root, session.ChatID)
	if err != nil {
		t.Fatalf("ReadEventLines: %v", err)
	}
	if len(lines) != 7 {
		t.Fatalf("event line count = %d, want 7: %#v", len(lines), lines)
	}
	for i, line := range lines {
		if line.RunID != session.RunID || line.LiveSeq != int64(i+1) {
			t.Fatalf("event line %d lost identity or cursor: %#v", i, line)
		}
	}
	if lines[5].Event["text"] != "hello world" {
		t.Fatalf("resumed content snapshot = %#v", lines[5].Event)
	}

	now := time.Now().UTC()
	request := approval.Request{
		ID: "approval-contract", RunID: session.RunID, Operation: "shell.execute",
		ToolCallID: "tool-contract", ToolName: "shell",
		Title: "Run tests", Risk: approval.RiskMedium, CreatedAt: now,
		Options: []approval.Option{{
			Action: approval.DecisionApprove, Scope: approval.ScopeRun, Label: "Approve",
		}},
	}
	bridge, err := NewApprovalEventBridge(
		PlatformRequestContext{
			RequestID: session.RequestID, ChatID: session.ChatID, AgentKey: session.AgentKey,
		},
		func(zenforge.Event, approval.Request) (string, error) { return "await-contract", nil },
		60,
	)
	if err != nil {
		t.Fatalf("NewApprovalEventBridge: %v", err)
	}
	askValue, err := bridge.Handle(requestedApprovalEvent(request, now))
	if err != nil {
		t.Fatalf("project approval.requested: %v", err)
	}
	ask := askValue.(AwaitingAsk)
	submit := RequestSubmit{
		Type: "request.submit", RequestID: session.RequestID, ChatID: session.ChatID,
		RunID: session.RunID, AgentKey: session.AgentKey, AwaitingID: ask.AwaitingID,
		SubmitID: "submit-contract",
		Params: []ApprovalParam{{
			ID: request.ID, Decision: PlatformDecisionApprove,
		}},
	}
	decision, err := DecisionFromRequestSubmit(ask, submit, now)
	if err != nil {
		t.Fatalf("DecisionFromRequestSubmit: %v", err)
	}
	if decision.RequestID != request.ID || decision.Action != approval.DecisionApprove ||
		decision.Scope != approval.ScopeRun {
		t.Fatalf("approval decision = %#v", decision)
	}
	resolved := zenforge.NewEvent(zenforge.EventApprovalResolved, session.RunID, map[string]any{
		"requestId": request.ID, "toolCallId": request.ToolCallID, "toolName": request.ToolName,
		"action": string(decision.Action), "scope": string(decision.Scope), "reason": decision.Reason,
	})
	resolved.Timestamp = now.UnixMilli()
	answerValue, err := bridge.Handle(resolved)
	if err != nil {
		t.Fatalf("project approval.resolved: %v", err)
	}
	answer := answerValue.(AwaitingAnswer)
	if answer.AwaitingID != ask.AwaitingID || answer.Status != PlatformStatusAnswered {
		t.Fatalf("approval answer = %#v", answer)
	}
}
