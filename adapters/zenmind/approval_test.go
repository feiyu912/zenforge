package zenmind

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/approval"
)

var fixtureTime = time.Date(2026, 6, 21, 8, 0, 0, 0, time.UTC)

func platformFixture(t *testing.T) (AwaitingAsk, RequestSubmit, approval.Decision, AwaitingAnswer) {
	t.Helper()
	req := approval.Request{
		ID: "approval_tool_7", RunID: "run_42", Operation: "shell.execute", Title: "git push origin main",
		Description: "Push the current branch", Risk: approval.RiskHigh, CreatedAt: fixtureTime,
		Options: []approval.Option{
			{Action: approval.DecisionApprove, Scope: approval.ScopeRun, Label: "Approve for run"},
			{Action: approval.DecisionApprove, Scope: approval.ScopeRule, Label: "Approve rule for run"},
			{Action: approval.DecisionReject, Scope: approval.ScopeOnce, Label: "Reject"},
		},
	}
	ask, err := AwaitingAskFromRequestContext(req, "await_tool_7", PlatformRequestContext{
		RequestID: "request_9", ChatID: "chat_3", AgentKey: "agent_ops",
	}, 120)
	if err != nil {
		t.Fatalf("AwaitingAskFromRequestContext: %v", err)
	}
	submit := RequestSubmit{
		Type: "request.submit", RequestID: "request_9", ChatID: "chat_3", RunID: req.RunID,
		AgentKey: "agent_ops", AwaitingID: ask.AwaitingID, SubmitID: "submit_11",
		Params: []ApprovalParam{{ID: req.ID, Decision: PlatformDecisionApproveRuleRun}},
	}
	decision, err := DecisionFromRequestSubmit(ask, submit, fixtureTime)
	if err != nil {
		t.Fatalf("DecisionFromRequestSubmit: %v", err)
	}
	answer, err := AwaitingAnswerFromDecision(ask, submit, decision)
	if err != nil {
		t.Fatalf("AwaitingAnswerFromDecision: %v", err)
	}
	return ask, submit, decision, answer
}

func TestAwaitingAskRequiresAgentKeyForCompletePlatformIdentity(t *testing.T) {
	ask, _, _, _ := platformFixture(t)
	ask.AgentKey = ""
	if err := ask.Validate(); err == nil || !strings.Contains(err.Error(), "agentKey") {
		t.Fatalf("expected missing agentKey error, got %v", err)
	}

	req := approval.Request{
		ID: "approval_legacy", RunID: "run_legacy", Operation: "shell.execute", Title: "echo ok",
		Risk: approval.RiskLow, CreatedAt: fixtureTime,
		Options: []approval.Option{{Action: approval.DecisionReject, Scope: approval.ScopeOnce, Label: "Reject"}},
	}
	legacy, err := AwaitingAskFromRequest(req, "await_legacy", 0)
	if err != nil {
		t.Fatalf("compatibility wrapper: %v", err)
	}
	if legacy.AgentKey != "" {
		t.Fatalf("legacy agentKey = %q, want empty", legacy.AgentKey)
	}
	if err := legacy.Validate(); err == nil || !strings.Contains(err.Error(), "agentKey") {
		t.Fatalf("legacy payload should require completion before wire use, got %v", err)
	}
	if _, err := AwaitingAskFromRequestContext(req, "await_legacy", PlatformRequestContext{}, 0); err == nil {
		t.Fatal("context-aware constructor accepted missing binding")
	}
	bound, err := BindAwaitingAsk(legacy, ApprovalBinding{
		RequestID: "request_legacy", ChatID: "chat_legacy", RunID: req.RunID,
		AgentKey: "agent_legacy", AwaitingID: "await_legacy",
	})
	if err != nil {
		t.Fatalf("BindAwaitingAsk: %v", err)
	}
	if err := bound.Validate(); err != nil {
		t.Fatalf("bound compatibility ask: %v", err)
	}
}

func TestAwaitingAskTimeoutBoundaries(t *testing.T) {
	ask, _, _, _ := platformFixture(t)
	for _, timeout := range []int64{0, 1, 120} {
		t.Run(fmt.Sprintf("timeout_%d", timeout), func(t *testing.T) {
			candidate := ask
			candidate.Timeout = timeout
			if err := candidate.Validate(); err != nil {
				t.Fatalf("timeout %d rejected: %v", timeout, err)
			}
		})
	}
	ask.Timeout = -1
	if err := ask.Validate(); err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected negative timeout error, got %v", err)
	}
}

func TestPlatformDecisionTranslationIsStrict(t *testing.T) {
	tests := []struct {
		name   string
		param  ApprovalParam
		action approval.DecisionAction
		scope  approval.DecisionScope
	}{
		{"approve run", ApprovalParam{ID: "a", Decision: PlatformDecisionApprove}, approval.DecisionApprove, approval.ScopeRun},
		{"approve rule", ApprovalParam{ID: "a", Decision: PlatformDecisionApproveRuleRun}, approval.DecisionApprove, approval.ScopeRule},
		{"reject", ApprovalParam{ID: "a", Decision: PlatformDecisionReject}, approval.DecisionReject, approval.ScopeOnce},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecisionFromPlatform(tc.param, fixtureTime)
			if err != nil {
				t.Fatal(err)
			}
			if got.Action != tc.action || got.Scope != tc.scope {
				t.Fatalf("got %#v", got)
			}
			roundtrip, err := DecisionToPlatform(got)
			if err != nil {
				t.Fatal(err)
			}
			if roundtrip != tc.param {
				t.Fatalf("roundtrip = %#v, want %#v", roundtrip, tc.param)
			}
		})
	}
	invalid := []approval.Decision{
		{RequestID: "a", Action: approval.DecisionApprove, Scope: approval.ScopeOnce},
		{RequestID: "a", Action: approval.DecisionReject, Scope: approval.ScopeRun},
		{RequestID: "a", Action: approval.DecisionAlways, Scope: approval.ScopeRule},
		{RequestID: "a", Action: approval.DecisionAbort, Scope: approval.ScopeOnce},
	}
	for _, decision := range invalid {
		if _, err := DecisionToPlatform(decision); err == nil {
			t.Fatalf("expected rejection for %#v", decision)
		}
	}
	if _, err := DecisionFromPlatform(ApprovalParam{ID: "a", Decision: "allow"}, fixtureTime); err == nil {
		t.Fatal("expected unknown platform decision to fail")
	}
}

func TestRequestSubmitRequiresIdentityAndExactApprovalIDs(t *testing.T) {
	ask, submit, _, _ := platformFixture(t)
	identityFields := []string{"requestId", "chatId", "runId", "agentKey", "awaitingId", "submitId"}
	for _, field := range identityFields {
		t.Run(field, func(t *testing.T) {
			bad := submit
			switch field {
			case "requestId":
				bad.RequestID = ""
			case "chatId":
				bad.ChatID = ""
			case "runId":
				bad.RunID = ""
			case "agentKey":
				bad.AgentKey = ""
			case "awaitingId":
				bad.AwaitingID = ""
			case "submitId":
				bad.SubmitID = ""
			}
			if _, err := DecisionFromRequestSubmit(ask, bad, fixtureTime); err == nil {
				t.Fatalf("expected missing %s to fail", field)
			}
		})
	}
	for name, params := range map[string][]ApprovalParam{
		"missing":   {{Decision: PlatformDecisionApprove}},
		"unknown":   {{ID: "other", Decision: PlatformDecisionApprove}},
		"duplicate": {{ID: "approval_tool_7", Decision: PlatformDecisionApprove}, {ID: "approval_tool_7", Decision: PlatformDecisionReject}},
	} {
		t.Run(name, func(t *testing.T) {
			bad := submit
			bad.Params = params
			if _, err := DecisionFromRequestSubmit(ask, bad, fixtureTime); err == nil {
				t.Fatal("expected id validation error")
			}
		})
	}
	badRun := submit
	badRun.RunID = "run_other"
	if _, err := DecisionFromRequestSubmit(ask, badRun, fixtureTime); err == nil {
		t.Fatal("expected run mismatch")
	}
	badAwaiting := submit
	badAwaiting.AwaitingID = "await_other"
	if _, err := DecisionFromRequestSubmit(ask, badAwaiting, fixtureTime); err == nil {
		t.Fatal("expected awaiting mismatch")
	}
	for name, mutate := range map[string]func(*RequestSubmit){
		"request": func(s *RequestSubmit) { s.RequestID = "request_other" },
		"chat":    func(s *RequestSubmit) { s.ChatID = "chat_other" },
		"agent":   func(s *RequestSubmit) { s.AgentKey = "agent_other" },
	} {
		t.Run(name+"_mismatch", func(t *testing.T) {
			bad := submit
			mutate(&bad)
			if _, err := DecisionFromRequestSubmit(ask, bad, fixtureTime); err == nil {
				t.Fatalf("expected %s mismatch", name)
			}
		})
	}
}

func TestSubmitRejectsUnboundCompatibilityAsk(t *testing.T) {
	ask, submit, _, _ := platformFixture(t)
	ask.Binding = ApprovalBinding{}
	if _, err := DecisionFromRequestSubmit(ask, submit, fixtureTime); err == nil ||
		!strings.Contains(err.Error(), "approval binding") {
		t.Fatalf("expected unbound ask rejection, got %v", err)
	}
}

func TestAwaitingAskRejectsMissingAndDuplicateApprovalIDs(t *testing.T) {
	ask, _, _, _ := platformFixture(t)
	ask.Approvals[0].ID = ""
	if err := ask.Validate(); err == nil {
		t.Fatal("expected missing id")
	}
	ask, _, _, _ = platformFixture(t)
	ask.Approvals = append(ask.Approvals, ask.Approvals[0])
	if err := ask.Validate(); err == nil {
		t.Fatal("expected duplicate id")
	}
}

func TestAwaitingAnswerTimeoutAndError(t *testing.T) {
	ask, _, _, _ := platformFixture(t)
	timeout, err := AwaitingErrorAnswer(ask, "", PlatformErrorTimeout, "等待项已超时")
	if err != nil {
		t.Fatal(err)
	}
	if timeout.Status != PlatformStatusError || timeout.Error == nil || timeout.Error.Code != PlatformErrorTimeout || len(timeout.Approvals) != 0 {
		t.Fatalf("unexpected timeout answer: %#v", timeout)
	}
	failed, err := AwaitingErrorAnswer(ask, "submit_12", "invalid_submit", "decision is required")
	if err != nil {
		t.Fatal(err)
	}
	if failed.SubmitID != "submit_12" || failed.Error.Code != "invalid_submit" {
		t.Fatalf("unexpected error answer: %#v", failed)
	}
}

func TestApprovalRoundTripGolden(t *testing.T) {
	ask, submit, decision, answer := platformFixture(t)
	if decision.RequestID != "approval_tool_7" || decision.Action != approval.DecisionApprove || decision.Scope != approval.ScopeRule {
		t.Fatalf("unexpected translated decision: %#v", decision)
	}
	wantFile, err := os.Open("testdata/platform/approval_roundtrip.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer wantFile.Close()
	scanner := bufio.NewScanner(wantFile)
	var want []any
	for scanner.Scan() {
		var value any
		if err := json.Unmarshal(scanner.Bytes(), &value); err != nil {
			t.Fatalf("decode golden: %v", err)
		}
		want = append(want, value)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(want) != 4 {
		t.Fatalf("golden lines = %d, want 4", len(want))
	}
	metadata, _ := want[0].(map[string]any)
	if metadata["sourceCommit"] != "1893edb51b8dc691ae974cea2719a835e0e21de4" {
		t.Fatalf("golden source commit = %#v", metadata["sourceCommit"])
	}
	sourceFiles, _ := metadata["sourceFiles"].([]any)
	if len(sourceFiles) != 3 {
		t.Fatalf("golden source files = %#v", metadata["sourceFiles"])
	}
	actual := []any{want[0], ask, submit, answer}
	var got []any
	for _, value := range actual {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var decoded any
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		got = append(got, decoded)
	}
	if !reflect.DeepEqual(got, want) {
		gotLines := make([]string, 0, len(actual))
		for _, value := range actual {
			data, _ := json.Marshal(value)
			gotLines = append(gotLines, string(data))
		}
		t.Fatalf("golden mismatch\ngot:\n%s", strings.Join(gotLines, "\n"))
	}
}
