package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/tools/askuser"
)

func TestCLIBrokerReadsDecision(t *testing.T) {
	req := approval.Request{
		ID:          "approval_1",
		RunID:       "run_1",
		Operation:   "shell.command",
		Title:       "Approve shell command",
		Description: "Run tests",
		Risk:        approval.RiskHigh,
		Options:     approval.DefaultOptions(),
		CreatedAt:   time.Now().UTC(),
	}
	var out bytes.Buffer
	decision, err := New(strings.NewReader("2\n"), &out).Request(context.Background(), req)
	if err != nil {
		t.Fatalf("Request returned error: %v", err)
	}
	if decision.Action != approval.DecisionReject || decision.RequestID != req.ID {
		t.Fatalf("decision = %#v", decision)
	}
	if !strings.Contains(out.String(), "Approval required: Approve shell command") {
		t.Fatalf("prompt output = %q", out.String())
	}
}

func sampleQuestionRequest(t *testing.T, payloadQuestions any) approval.Request {
	t.Helper()
	return approval.Request{
		ID:          "approval_q1",
		RunID:       "run_1",
		ToolCallID:  "call_1",
		ToolName:    "ask_user",
		Operation:   askuser.Operation,
		Title:       "Answer the agent's questions",
		Description: "1. Which mode?",
		Risk:        approval.RiskLow,
		Options:     approval.DefaultOptions(),
		Payload: map[string]any{
			"questions":   payloadQuestions,
			"fingerprint": "fp",
		},
		CreatedAt: time.Now().UTC(),
	}
}

func sampleQuestions() []askuser.Question {
	return []askuser.Question{
		{
			ID:       "mode",
			Question: "Which mode?",
			Header:   "Choose Mode",
			Options:  []askuser.Option{{Label: "fast"}, {Label: "safe", Description: "verifies"}},
		},
		{
			ID:          "tools",
			Question:    "Which tools?",
			MultiSelect: true,
			Options:     []askuser.Option{{Label: "shell"}, {Label: "web"}},
		},
		{
			ID:       "why",
			Question: "Why do you need this?",
		},
	}
}

func TestCLIBrokerAnswersUserQuestions(t *testing.T) {
	req := sampleQuestionRequest(t, sampleQuestions())
	var out bytes.Buffer
	broker := New(strings.NewReader("2\n1,2\nfree text\n"), &out)
	decision, err := broker.Request(context.Background(), req)
	if err != nil {
		t.Fatalf("Request returned error: %v", err)
	}
	if decision.Action != approval.DecisionApprove {
		t.Fatalf("decision action = %q, want approve", decision.Action)
	}
	answers, _ := decision.Payload[askuser.AnswersPayloadKey].(map[string]any)
	if answers["mode"] != "safe" {
		t.Fatalf("mode answer = %#v, want safe", answers["mode"])
	}
	tools, ok := answers["tools"].([]string)
	if !ok || len(tools) != 2 || tools[0] != "shell" || tools[1] != "web" {
		t.Fatalf("tools answer = %#v, want [shell web]", answers["tools"])
	}
	if answers["why"] != "free text" {
		t.Fatalf("why answer = %#v, want free text", answers["why"])
	}
	rendered := out.String()
	for _, want := range []string{"== Choose Mode ==", "Question [mode]: Which mode?", "2. safe — verifies", "Select numbers (comma-separated)"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("broker output missing %q:\n%s", want, rendered)
		}
	}
}

func TestCLIBrokerAnswersQuestionsFromGenericPayload(t *testing.T) {
	encoded, err := json.Marshal(sampleQuestions())
	if err != nil {
		t.Fatalf("marshal questions: %v", err)
	}
	var generic []any
	if err := json.Unmarshal(encoded, &generic); err != nil {
		t.Fatalf("unmarshal questions: %v", err)
	}
	req := sampleQuestionRequest(t, generic)
	broker := New(strings.NewReader("1\nshell\nbecause\n"), &bytes.Buffer{})
	decision, err := broker.Request(context.Background(), req)
	if err != nil {
		t.Fatalf("Request returned error: %v", err)
	}
	answers, _ := decision.Payload[askuser.AnswersPayloadKey].(map[string]any)
	if answers["mode"] != "fast" || answers["why"] != "because" {
		t.Fatalf("generic payload answers = %#v", answers)
	}
}

func TestCLIBrokerRejectsInvalidQuestionAnswers(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		message string
	}{
		{"out of range choice", "9\n", "invalid choice"},
		{"empty answer", "\n", "needs an answer"},
		{"bad multi-select part", "fast\nshell,7\n", "invalid choice"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := sampleQuestionRequest(t, sampleQuestions())
			broker := New(strings.NewReader(tc.input), &bytes.Buffer{})
			_, err := broker.Request(context.Background(), req)
			if err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("Request error = %v, want it to contain %q", err, tc.message)
			}
		})
	}
}

func TestCLIBrokerQuestionRequestWithoutQuestionsFails(t *testing.T) {
	req := sampleQuestionRequest(t, nil)
	broker := New(strings.NewReader(""), &bytes.Buffer{})
	if _, err := broker.Request(context.Background(), req); err == nil || !strings.Contains(err.Error(), "carries no questions") {
		t.Fatalf("Request error = %v, want missing-questions failure", err)
	}
}

// TestCLIBrokerKeepsTheAnswersAFullRunWasGiven pins what a scripted or piped
// operator depends on: a broker built by New reads every prompt from one
// buffer. Reading each prompt with its own bufio.Reader swallowed the rest of
// the input ahead of the next prompt, so a caller that wrote two answers up
// front had the second prompt read EOF and the run fail -- the exact shape of
// a CI job that drives an approval-gated example.
func TestCLIBrokerKeepsTheAnswersAFullRunWasGiven(t *testing.T) {
	requests := []approval.Request{
		{
			ID: "approval_write", RunID: "run_1", Operation: "workspace.write",
			Title: "Approve workspace write", Risk: approval.RiskHigh,
			Options: approval.DefaultOptions(), CreatedAt: time.Now().UTC(),
		},
		{
			ID: "approval_shell", RunID: "run_1", Operation: "shell.command",
			Title: "Approve shell command", Risk: approval.RiskHigh,
			Options: approval.DefaultOptions(), CreatedAt: time.Now().UTC(),
		},
	}
	// Both answers are written before the first prompt is answered.
	broker := New(strings.NewReader("1\n2\n"), io.Discard)
	for index, req := range requests {
		decision, err := broker.Request(context.Background(), req)
		if err != nil {
			t.Fatalf("prompt %d returned error: %v", index+1, err)
		}
		want := approval.DecisionApprove
		if index == 1 {
			want = approval.DecisionReject
		}
		if decision.Action != want {
			t.Fatalf("prompt %d answered %q, want %q", index+1, decision.Action, want)
		}
	}
}

// TestCLIBrokerReadsOneAnswerPerPromptFromALiteral checks the same property for
// a Broker assembled without New: the first prompt's reader may consume what it
// can, but the second prompt still reads what it was given.
func TestCLIBrokerReadsOneAnswerPerPromptFromALiteral(t *testing.T) {
	req := approval.Request{
		ID: "approval_1", RunID: "run_1", Operation: "shell.command",
		Title: "Approve shell command", Risk: approval.RiskHigh,
		Options: approval.DefaultOptions(), CreatedAt: time.Now().UTC(),
	}
	broker := Broker{In: strings.NewReader("1\n"), Out: io.Discard}
	decision, err := broker.Request(context.Background(), req)
	if err != nil {
		t.Fatalf("Request returned error: %v", err)
	}
	if decision.Action != approval.DecisionApprove {
		t.Fatalf("action = %q, want approve", decision.Action)
	}
}
