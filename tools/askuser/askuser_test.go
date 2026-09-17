package askuser

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/tool"
)

func askArgs(t *testing.T, questions ...Question) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(input{Questions: questions})
	if err != nil {
		t.Fatalf("marshal questions: %v", err)
	}
	return encoded
}

func sampleQuestions() []Question {
	return []Question{
		{
			ID:       "mode",
			Question: "Which mode should the migration use?",
			Header:   "Choose Mode",
			Options: []Option{
				{Label: "fast (Recommended)", Description: "Skips verification."},
				{Label: "safe", Description: "Verifies every step."},
			},
		},
		{
			ID:          "tools",
			Question:    "Which tools should stay enabled?",
			MultiSelect: true,
			Options:     []Option{{Label: "shell"}, {Label: "web"}},
		},
	}
}

func TestAskUserRequiresApprovalShape(t *testing.T) {
	askTool := Must(Config{})
	result, err := askTool.Call(context.Background(), askArgs(t, sampleQuestions()...), tool.Context{RunID: "run_1", ToolCallID: "call_1"})
	if !errors.Is(err, approval.ErrRequired) {
		t.Fatalf("Call error = %v, want approval.ErrRequired", err)
	}
	request, ok := approval.RequestFromResult(result)
	if !ok {
		t.Fatalf("result carried no approval request: %#v", result)
	}
	if request.Operation != Operation || request.ToolName != "ask_user" {
		t.Fatalf("unexpected request identity: %+v", request)
	}
	if request.Risk != approval.RiskLow {
		t.Fatalf("question risk = %q, want low", request.Risk)
	}
	if !strings.Contains(request.Description, "Which mode should the migration use?") ||
		!strings.Contains(request.Description, "fast (Recommended) | safe") {
		t.Fatalf("description digest incomplete: %q", request.Description)
	}
	fingerprint, _ := request.Payload["fingerprint"].(string)
	if fingerprint == "" {
		t.Fatalf("request payload missing fingerprint: %#v", request.Payload)
	}
	questions, _ := request.Payload["questions"].([]Question)
	if len(questions) != 2 || questions[0].ID != "mode" {
		t.Fatalf("request payload lost questions: %#v", request.Payload["questions"])
	}
	if request.Payload["answersKey"] != AnswersPayloadKey {
		t.Fatalf("request payload missing answers key hint: %#v", request.Payload)
	}
}

func TestAskUserFingerprintIsStablePerQuestionSet(t *testing.T) {
	askTool := Must(Config{})
	first, err := askTool.Call(context.Background(), askArgs(t, sampleQuestions()...), tool.Context{RunID: "run_1", ToolCallID: "call_1"})
	if !errors.Is(err, approval.ErrRequired) {
		t.Fatalf("first Call error = %v", err)
	}
	second, err := askTool.Call(context.Background(), askArgs(t, sampleQuestions()...), tool.Context{RunID: "run_2", ToolCallID: "call_9"})
	if !errors.Is(err, approval.ErrRequired) {
		t.Fatalf("second Call error = %v", err)
	}
	firstRequest, _ := approval.RequestFromResult(first)
	secondRequest, _ := approval.RequestFromResult(second)
	if firstRequest.Payload["fingerprint"] != secondRequest.Payload["fingerprint"] {
		t.Fatalf("identical question sets produced different fingerprints")
	}
	changed := sampleQuestions()
	changed[0].Question = "Something else?"
	third, err := askTool.Call(context.Background(), askArgs(t, changed...), tool.Context{RunID: "run_3", ToolCallID: "call_1"})
	if !errors.Is(err, approval.ErrRequired) {
		t.Fatalf("third Call error = %v", err)
	}
	thirdRequest, _ := approval.RequestFromResult(third)
	if firstRequest.Payload["fingerprint"] == thirdRequest.Payload["fingerprint"] {
		t.Fatalf("different question sets shared a fingerprint")
	}
}

func TestAskUserAnswersFromApprovedMetadata(t *testing.T) {
	askTool := Must(Config{})
	questions := sampleQuestions()
	required, err := askTool.Call(context.Background(), askArgs(t, questions...), tool.Context{RunID: "run_1", ToolCallID: "call_1"})
	if !errors.Is(err, approval.ErrRequired) {
		t.Fatalf("Call error = %v, want approval.ErrRequired", err)
	}
	request, _ := approval.RequestFromResult(required)
	metadata := approval.ApprovedMetadata(nil, request, approval.Decision{
		Action: approval.DecisionApprove,
		Payload: map[string]any{
			AnswersPayloadKey: map[string]any{
				"mode":  "safe",
				"tools": []any{"shell", "web"},
			},
		},
	})
	result, err := askTool.Call(context.Background(), askArgs(t, questions...), tool.Context{RunID: "run_1", ToolCallID: "call_1", Metadata: metadata})
	if err != nil {
		t.Fatalf("answered Call returned error: %v", err)
	}
	answers, ok := result.Structured["answers"].(map[string]any)
	if !ok || answers["mode"] != "safe" {
		t.Fatalf("structured answers missing: %#v", result.Structured)
	}
	output := result.Output
	if !strings.Contains(output, "- mode: safe") || !strings.Contains(output, "- tools: shell, web") {
		t.Fatalf("rendered answers incomplete: %q", output)
	}
	if strings.Index(output, "- mode") > strings.Index(output, "- tools") {
		t.Fatalf("answers not rendered in stable id order: %q", output)
	}
}

func TestAskUserApprovedWithoutAnswersReportsDismissal(t *testing.T) {
	askTool := Must(Config{})
	questions := sampleQuestions()
	required, err := askTool.Call(context.Background(), askArgs(t, questions...), tool.Context{RunID: "run_1", ToolCallID: "call_1"})
	if !errors.Is(err, approval.ErrRequired) {
		t.Fatalf("Call error = %v", err)
	}
	request, _ := approval.RequestFromResult(required)
	metadata := approval.ApprovedMetadata(nil, request, approval.Decision{Action: approval.DecisionApprove})
	result, err := askTool.Call(context.Background(), askArgs(t, questions...), tool.Context{RunID: "run_1", ToolCallID: "call_1", Metadata: metadata})
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(result.Output, "without providing answers") {
		t.Fatalf("dismissal not reported: %q", result.Output)
	}
}

func TestAskUserRejectedInsideSubagents(t *testing.T) {
	askTool := Must(Config{})
	result, err := askTool.Call(context.Background(), askArgs(t, sampleQuestions()...), tool.Context{
		RunID:    "run_1",
		Metadata: map[string]any{"subtaskId": "task_1"},
	})
	if !errors.Is(err, tool.ErrInvalidArguments) {
		t.Fatalf("Call error = %v, want ErrInvalidArguments", err)
	}
	if !strings.Contains(result.Error, "not available inside subagents") {
		t.Fatalf("delegated-caller guidance missing: %q", result.Error)
	}
}

func TestAskUserValidation(t *testing.T) {
	cases := []struct {
		name      string
		config    Config
		questions []Question
		message   string
	}{
		{"no questions", Config{}, nil, "at least one question"},
		{"over limit", Config{MaxQuestions: 1}, sampleQuestions(), "per-call limit"},
		{"empty id", Config{}, []Question{{ID: " ", Question: "why?"}}, "stable non-empty id"},
		{"duplicate ids", Config{}, []Question{{ID: "a", Question: "one?"}, {ID: "a", Question: "two?"}}, "duplicate question id"},
		{"empty question text", Config{}, []Question{{ID: "a", Question: "  "}}, "empty question text"},
		{"empty option label", Config{}, []Question{{ID: "a", Question: "pick", Options: []Option{{Label: " "}}}}, "empty label"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			askTool := Must(tc.config)
			result, err := askTool.Call(context.Background(), askArgs(t, tc.questions...), tool.Context{RunID: "run_1", ToolCallID: "call_1"})
			if !errors.Is(err, tool.ErrInvalidArguments) {
				t.Fatalf("Call error = %v, want ErrInvalidArguments", err)
			}
			if !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("Call error = %q, want it to contain %q", err, tc.message)
			}
			if result.ExitCode != 1 {
				t.Fatalf("validation exit code = %d, want 1", result.ExitCode)
			}
		})
	}
	if _, err := New(Config{MaxQuestions: -1}); err == nil {
		t.Fatalf("negative MaxQuestions accepted")
	}
}

func TestAskUserSchemaExposesQuestions(t *testing.T) {
	askTool := Must(Config{})
	if askTool.Name() != "ask_user" {
		t.Fatalf("tool name = %q", askTool.Name())
	}
	props, ok := askTool.Schema()["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no properties: %#v", askTool.Schema())
	}
	if _, ok := props["questions"]; !ok {
		t.Fatalf("schema missing questions property: %#v", props)
	}
}
