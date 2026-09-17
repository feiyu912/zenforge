package review

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/model"
)

func TestParseVerdictAcceptsTheReferenceShape(t *testing.T) {
	verdict, err := ParseVerdict(`{"decision":"request_changes","summary":"missing validation","findings":[
		{"severity":"high","path":"internal/x.go","line":42,"issue":"unchecked error\nfrom Write","suggestion":"check and return it"},
		{"severity":"low","path":"README.md","issue":"docs not updated"},
		{"severity":"","issue":"medium default"}
	]}`)
	if err != nil {
		t.Fatalf("ParseVerdict returned error: %v", err)
	}
	if verdict.Decision != DecisionRequestChanges || verdict.Summary != "missing validation" {
		t.Fatalf("verdict = %#v", verdict)
	}
	if len(verdict.Findings) != 3 {
		t.Fatalf("findings = %#v", verdict.Findings)
	}
	// Worst first, and multi-line issues are flattened so a finding stays
	// one injectable line.
	if verdict.Findings[0].Severity != SeverityHigh || strings.Contains(verdict.Findings[0].Issue, "\n") {
		t.Fatalf("findings = %#v", verdict.Findings)
	}
	if verdict.Findings[1].Severity != SeverityMedium {
		t.Fatalf("a missing severity did not default to medium: %#v", verdict.Findings)
	}
	if len(verdict.Blocking()) != 1 {
		t.Fatalf("blocking = %#v", verdict.Blocking())
	}
	if !verdict.RequestsChanges() {
		t.Fatal("request_changes was not recognised")
	}
}

func TestParseVerdictRejectsUnusableAnswers(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"not json", "The code looks fine to me."},
		{"broken json", `{"decision":"approve"`},
		{"unknown decision", `{"decision":"maybe"}`},
		{"unknown severity", `{"decision":"comment","findings":[{"severity":"cosmic","issue":"x"}]}`},
		{"changes without a reason", `{"decision":"request_changes"}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := ParseVerdict(testCase.input); err == nil {
				t.Fatalf("output %q was accepted", testCase.input)
			}
		})
	}
	// A malformed verdict must never read as approval: the caller sees an
	// error, not a zero-value approve.
	if verdict, err := ParseVerdict(`{"decision":"approve","findings":[{"issue":"  "}]}`); err != nil || len(verdict.Findings) != 0 {
		t.Fatalf("verdict = %#v, %v", verdict, err)
	}
}

func TestModelReviewerSendsTheRunAndParsesTheAnswer(t *testing.T) {
	fake := &scriptedReviewModel{response: "```json\n{\"decision\":\"approve\",\"summary\":\"no problems\"}\n```"}
	reviewer := ModelReviewer{Model: fake, Label: "guardian"}
	verdict, err := reviewer.Review(context.Background(), Request{
		Task: "fix the build", Output: "fixed", Files: []string{"main.go"}, Diff: "--- a/main.go\n+++ b/main.go\n",
	})
	if err != nil {
		t.Fatalf("Review returned error: %v", err)
	}
	if verdict.Decision != DecisionApprove || verdict.Model != "guardian" {
		t.Fatalf("verdict = %#v", verdict)
	}
	if len(fake.requests) != 1 || fake.requests[0].ToolChoice != model.ToolChoiceNone {
		t.Fatalf("requests = %#v", fake.requests)
	}
	sent := fake.requests[0].Messages[1].Content
	for _, want := range []string{"fix the build", "fixed", "main.go", "+++ b/main.go"} {
		if !strings.Contains(sent, want) {
			t.Fatalf("the review input is missing %q: %q", want, sent)
		}
	}
	// Without a model the reviewer refuses rather than approving by default.
	if _, err := (ModelReviewer{}).Review(context.Background(), Request{}); err == nil {
		t.Fatal("a reviewer without a model ran")
	}
	if _, err := (ModelReviewer{Model: &scriptedReviewModel{err: errors.New("down")}}).Review(context.Background(), Request{}); err == nil {
		t.Fatal("a model error was ignored")
	}
}

func TestFormatFindingsIsActionableAndBounded(t *testing.T) {
	verdict := Verdict{
		Decision: DecisionRequestChanges,
		Summary:  "two problems",
		Findings: []Finding{
			{Severity: SeverityHigh, Path: "a.go", Line: 12, Issue: "leaks the file", Suggestion: "defer Close()"},
			{Severity: SeverityLow, Issue: "naming", Suggestion: "rename"},
			{Severity: SeverityMedium, Issue: strings.Repeat("x", 500)},
		},
	}
	rendered := FormatFindings(verdict, 0)
	if !strings.Contains(rendered, "must be fixed before you finish") || !strings.Contains(rendered, "a.go:12") {
		t.Fatalf("rendered = %q", rendered)
	}
	if !strings.Contains(rendered, "-> defer Close()") {
		t.Fatalf("the suggestion is missing: %q", rendered)
	}
	// The instruction is a bounded injection, not a dump.
	tight := FormatFindings(verdict, 200)
	if len(tight) > 400 || !strings.Contains(tight, "omitted") {
		t.Fatalf("tight = %q", tight)
	}
	// Counting is stable for logs.
	counts := SeverityCounts(verdict)
	if counts["high"] != 1 || counts["low"] != 1 || counts["medium"] != 1 {
		t.Fatalf("counts = %#v", counts)
	}
}

func TestGuardianModes(t *testing.T) {
	fake := &scriptedReviewModel{response: `{"decision":"request_changes","summary":"nope","findings":[{"severity":"critical","issue":"deletes user data"}]}`}
	enforcing := &Guardian{Reviewer: ModelReviewer{Model: fake}, Mode: ModeEnforce}
	result, done := enforcing.Review(context.Background(), Request{Task: "t"})
	if !done || !result.Enforced || !strings.Contains(result.Instruction, "deletes user data") {
		t.Fatalf("result = %#v done = %v", result, done)
	}
	// Report mode records the same verdict and lets the run finish.
	reporting := &Guardian{Reviewer: ModelReviewer{Model: fake}, Mode: ModeReport}
	result, done = reporting.Review(context.Background(), Request{Task: "t"})
	if !done || result.Enforced || result.Instruction != "" {
		t.Fatalf("report mode enforced: %#v", result)
	}
	// An approving reviewer never enforces, even in enforce mode.
	approving := &Guardian{Reviewer: ModelReviewer{Model: &scriptedReviewModel{response: `{"decision":"approve"}`}}, Mode: ModeEnforce}
	if result, done := approving.Review(context.Background(), Request{}); !done || result.Enforced {
		t.Fatalf("an approval enforced: %#v", result)
	}
	// Not configured means no review at all.
	if _, done := (&Guardian{}).Review(context.Background(), Request{}); done {
		t.Fatal("a guardian without a reviewer reported a review")
	}
	if _, done := (*Guardian)(nil).Review(context.Background(), Request{}); done {
		t.Fatal("a nil guardian reported a review")
	}
	// A broken reviewer is reported, not an approval, and never enforces.
	broken := &Guardian{Reviewer: ModelReviewer{Model: &scriptedReviewModel{err: errors.New("reviewer down")}}, Mode: ModeEnforce}
	result, done = broken.Review(context.Background(), Request{})
	if !done || result.Enforced || !strings.Contains(result.Verdict.Summary, "review failed") {
		t.Fatalf("a failed review was treated as a verdict: %#v", result)
	}
}

func TestParseModeAndDecision(t *testing.T) {
	for name, want := range map[string]Mode{"": ModeOff, "off": ModeOff, "report": ModeReport, "ON": ModeReport, "enforce": ModeEnforce, "strict": ModeEnforce} {
		got, err := ParseMode(name)
		if err != nil || got != want {
			t.Fatalf("ParseMode(%q) = %q, %v", name, got, err)
		}
	}
	if _, err := ParseMode("maybe"); err == nil {
		t.Fatal("an unknown mode was accepted")
	}
	for name, want := range map[string]Decision{"approve": DecisionApprove, "LGTM": DecisionApprove, "request-changes": DecisionRequestChanges, "comment": DecisionComment} {
		got, err := ParseDecision(name)
		if err != nil || got != want {
			t.Fatalf("ParseDecision(%q) = %q, %v", name, got, err)
		}
	}
	if _, err := ParseDecision("shipit"); err == nil {
		t.Fatal("an unknown decision was accepted")
	}
	if !SeverityCritical.Blocks() || !SeverityHigh.Blocks() || SeverityMedium.Blocks() || SeverityLow.Blocks() {
		t.Fatal("Blocking() does not follow the severity ladder")
	}
}

func TestFormatRequestBoundsTheDiff(t *testing.T) {
	request := Request{
		Task:   strings.Repeat("t", 5000),
		Output: strings.Repeat("o", 5000),
		Diff:   strings.Repeat("d", 30000),
	}
	for index := 0; index < 100; index++ {
		request.Files = append(request.Files, "/file")
		request.Commands = append(request.Commands, "go test ./...")
		request.Failures = append(request.Failures, "boom")
	}
	rendered := FormatRequest(request)
	if len(rendered) > 40000 {
		t.Fatalf("rendered input is %d bytes", len(rendered))
	}
	if !strings.Contains(rendered, "[truncated]") {
		t.Fatalf("truncation was not marked")
	}
	if strings.Count(rendered, "- /file") != 1 {
		t.Fatalf("files were not deduplicated: %d", strings.Count(rendered, "- /file"))
	}
}

// scriptedReviewModel answers Generate with a canned response.
type scriptedReviewModel struct {
	response string
	err      error
	requests []model.Request
}

func (m *scriptedReviewModel) Generate(_ context.Context, req model.Request) (*model.Response, error) {
	m.requests = append(m.requests, req)
	if m.err != nil {
		return nil, m.err
	}
	return &model.Response{Message: model.Message{Role: "assistant", Content: m.response}}, nil
}

func (m *scriptedReviewModel) Stream(context.Context, model.Request) (<-chan model.Event, error) {
	return nil, errors.New("Stream is not used")
}
