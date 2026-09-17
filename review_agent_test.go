package zenforge

import (
	"context"
	"strings"
	"testing"

	checkpointmemory "github.com/feiyu912/zenforge/checkpoint/memory"
	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/review"
)

// scriptedReviewModel answers Generate with canned verdicts, one per call;
// the last verdict repeats.
type scriptedReviewModel struct {
	response  string
	responses []string
	requests  []model.Request
}

func (m *scriptedReviewModel) Generate(_ context.Context, req model.Request) (*model.Response, error) {
	m.requests = append(m.requests, req)
	response := m.response
	if len(m.responses) > 0 {
		response = m.responses[0]
		m.responses = m.responses[1:]
	}
	return &model.Response{Message: model.Message{Role: "assistant", Content: response}}, nil
}

func (m *scriptedReviewModel) Stream(context.Context, model.Request) (<-chan model.Event, error) {
	return nil, nil
}

// reviewOnRunWithChanges builds an agent whose review sees a real turn.diff:
// the answer is recorded as a workspace change, so the reviewer has
// something to look at.
func reviewOnRun(t *testing.T, guardian *review.Guardian, turns ...scriptedTurn) (*Agent, *scriptedReviewModel) {
	t.Helper()
	fake := &scriptedReviewModel{response: `{"decision":"approve"}`}
	agent := New(Config{
		Model:       &scriptedModel{turns: turns},
		Events:      &testEventStore{},
		Checkpoints: checkpointmemory.New(),
		WorkingDir:  t.TempDir(),
		MaxSteps:    3,
		Review:      guardian,
	})
	return agent, fake
}

func TestReviewReportModeRecordsTheVerdict(t *testing.T) {
	ctx := context.Background()
	reviewer := &scriptedReviewModel{response: `{"decision":"request_changes","summary":"missing tests","findings":[{"severity":"high","path":"main.go","issue":"no test covers this","suggestion":"add one"}]}`}
	agent, _ := reviewOnRun(t, &review.Guardian{Reviewer: review.ModelReviewer{Model: reviewer, Label: "guardian"}, Mode: review.ModeReport},
		scriptedTurn{events: []model.Event{{Delta: "the answer"}}})
	stream, err := agent.Stream(ctx, Task{Input: "fix it"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	done := eventsByType(collected, EventRunDone)
	if len(done) != 1 || stringValue(done[0].Payload["output"]) != "the answer" {
		t.Fatalf("report mode blocked the run: %v", collected)
	}
	reviews := eventsByType(collected, EventReviewCompleted)
	if len(reviews) != 1 {
		t.Fatalf("review.completed events = %d: %v", len(reviews), collected)
	}
	payload := reviews[0].Payload
	if payload["decision"] != "request_changes" || payload["enforced"] != false {
		t.Fatalf("payload = %#v", payload)
	}
	severities, _ := payload["severities"].(map[string]int)
	if severities["high"] != 1 {
		t.Fatalf("severities = %#v", payload["severities"])
	}
	if len(reviewer.requests) != 1 {
		t.Fatalf("review requests = %d", len(reviewer.requests))
	}
	// The reviewer saw the task and the answer even with no diff events.
	sent := reviewer.requests[0].Messages[1].Content
	if !strings.Contains(sent, "fix it") || !strings.Contains(sent, "the answer") {
		t.Fatalf("review input = %q", sent)
	}
}

func TestReviewEnforceModeSendsTheAgentBackToWork(t *testing.T) {
	ctx := context.Background()
	// The first review asks for changes; the second approves, so the run
	// demonstrably continues and then finishes.
	reviewer := &scriptedReviewModel{responses: []string{
		`{"decision":"request_changes","summary":"unsafe","findings":[{"severity":"critical","path":"main.go","issue":"deletes user data","suggestion":"guard the delete"}]}`,
		`{"decision":"approve","summary":"fixed"}`,
	}}
	agent, _ := reviewOnRun(t, &review.Guardian{Reviewer: review.ModelReviewer{Model: reviewer}, Mode: review.ModeEnforce},
		scriptedTurn{events: []model.Event{{Delta: "first answer"}}},
		scriptedTurn{events: []model.Event{{Delta: "second answer"}}})
	stream, err := agent.Stream(ctx, Task{Input: "fix it"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	done := eventsByType(collected, EventRunDone)
	if len(done) != 1 || stringValue(done[0].Payload["output"]) != "second answer" {
		t.Fatalf("the enforced review did not force another turn: %v", collected)
	}
	// The findings became the agent's newest instruction.
	scripted, ok := agent.config.Model.(*scriptedModel)
	if !ok {
		t.Fatalf("model = %T", agent.config.Model)
	}
	last := scripted.requests[len(scripted.requests)-1]
	found := false
	for _, message := range last.Messages {
		if message.Role == "user" && strings.Contains(message.Content, "deletes user data") && strings.Contains(message.Content, "guard the delete") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the findings were not sent to the model: %#v", last.Messages)
	}
	// A Stop hook's bounded refusals also bound the reviewer, so a reviewer
	// cannot hold the run forever.
	if len(scripted.requests) != 2 {
		t.Fatalf("model requests = %d, want 2", len(scripted.requests))
	}
}

func TestReviewEnforceModeLetsAnApprovalFinish(t *testing.T) {
	ctx := context.Background()
	reviewer := &scriptedReviewModel{response: `{"decision":"approve","summary":"looks correct"}`}
	agent, _ := reviewOnRun(t, &review.Guardian{Reviewer: review.ModelReviewer{Model: reviewer}, Mode: review.ModeEnforce},
		scriptedTurn{events: []model.Event{{Delta: "the answer"}}})
	stream, err := agent.Stream(ctx, Task{Input: "fix it"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	if len(eventsByType(collected, EventRunDone)) != 1 {
		t.Fatalf("an approval blocked the run: %v", collected)
	}
	reviews := eventsByType(collected, EventReviewCompleted)
	if len(reviews) != 1 || reviews[0].Payload["decision"] != "approve" {
		t.Fatalf("reviews = %#v", reviews)
	}
}

func TestBrokenReviewerNeverBlocksTheRun(t *testing.T) {
	ctx := context.Background()
	// Unparsable output is a failed review, not an approval and not a block.
	reviewer := &scriptedReviewModel{response: "I think it is fine."}
	agent, _ := reviewOnRun(t, &review.Guardian{Reviewer: review.ModelReviewer{Model: reviewer}, Mode: review.ModeEnforce},
		scriptedTurn{events: []model.Event{{Delta: "the answer"}}})
	stream, err := agent.Stream(ctx, Task{Input: "fix it"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	if len(eventsByType(collected, EventRunDone)) != 1 {
		t.Fatalf("a broken reviewer blocked the run: %v", collected)
	}
	reviews := eventsByType(collected, EventReviewCompleted)
	if len(reviews) != 1 || !strings.Contains(stringValue(reviews[0].Payload["summary"]), "review failed") {
		t.Fatalf("reviews = %#v", reviews)
	}
}

func TestNoGuardianMeansNoReview(t *testing.T) {
	ctx := context.Background()
	agent, _ := reviewOnRun(t, nil, scriptedTurn{events: []model.Event{{Delta: "done"}}})
	stream, err := agent.Stream(ctx, Task{Input: "fix it"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	if len(eventsByType(collected, EventReviewCompleted)) != 0 {
		t.Fatalf("a review ran without a guardian: %v", collected)
	}
}

// readableEventStore serves the events it recorded, so the review path that
// reads the run's turn.diff events is exercised for real.
type readableEventStore struct {
	events []Event
}

func (s *readableEventStore) Append(ctx context.Context, event Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.events = append(s.events, event)
	return nil
}

func (s *readableEventStore) Read(ctx context.Context, runID string, afterSeq int64, limit int) ([]Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []Event
	for _, event := range s.events {
		if event.RunID() != runID || event.Seq <= afterSeq {
			continue
		}
		out = append(out, event)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *readableEventStore) LatestSeq(ctx context.Context, runID string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	latest := int64(0)
	for _, event := range s.events {
		if event.RunID() == runID && event.Seq > latest {
			latest = event.Seq
		}
	}
	return latest, nil
}

func TestReviewSeesTheRunsDiff(t *testing.T) {
	ctx := context.Background()
	events := &readableEventStore{}
	// Seed the run's own history with a diff and a command, as the turn-diff
	// stream and the tool events would have.
	seq := int64(0)
	for _, event := range []Event{
		NewEvent(EventTurnDiff, "run_review", map[string]any{
			"fileCount": 1,
			"paths":     []string{"main.go"},
			"files": []any{map[string]any{
				"path": "main.go",
				"diff": "--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n",
			}},
		}),
		NewEvent(EventToolCall, "run_review", map[string]any{"toolName": "shell", "arguments": `{"command":"go test ./..."}`}),
		NewEvent(EventToolError, "run_review", map[string]any{"error": "test failed"}),
	} {
		seq = NextEventSeq(seq)
		if err := events.Append(ctx, event.WithSeq(seq)); err != nil {
			t.Fatalf("Append returned error: %v", err)
		}
	}
	reviewer := &scriptedReviewModel{response: `{"decision":"approve"}`}
	agent := New(Config{
		Model:       &scriptedModel{turns: []scriptedTurn{{events: []model.Event{{Delta: "the answer"}}}}},
		Events:      events,
		Checkpoints: checkpointmemory.New(),
		WorkingDir:  t.TempDir(),
		MaxSteps:    2,
		Review:      &review.Guardian{Reviewer: review.ModelReviewer{Model: reviewer}, Mode: review.ModeReport},
	})
	stream, err := agent.Stream(ctx, Task{RunID: "run_review", Input: "fix it"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collectRunEvents(t, stream)
	if len(reviewer.requests) != 1 {
		t.Fatalf("review requests = %d", len(reviewer.requests))
	}
	sent := reviewer.requests[0].Messages[1].Content
	for _, want := range []string{"+new", "go test ./...", "test failed", "main.go"} {
		if !strings.Contains(sent, want) {
			t.Fatalf("the review input is missing %q:\n%s", want, sent)
		}
	}
}
