package zenforge_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/feiyu912/zenforge"
	checkpointsqlite "github.com/feiyu912/zenforge/checkpoint/sqlite"
	eventlogsqlite "github.com/feiyu912/zenforge/eventlog/sqlite"
	"github.com/feiyu912/zenforge/model"
)

func TestSQLiteDurableRunSoak(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runs.db")
	events, err := eventlogsqlite.Open(ctx, path)
	if err != nil {
		t.Fatalf("open event store: %v", err)
	}
	defer events.Close()
	checkpoints, err := checkpointsqlite.Open(ctx, path)
	if err != nil {
		t.Fatalf("open checkpoint store: %v", err)
	}
	defer checkpoints.Close()

	agent := zenforge.New(zenforge.Config{
		Model:       staticModel{output: "soak complete"},
		Events:      events,
		Checkpoints: checkpoints,
		MaxSteps:    2,
	})

	for i := 0; i < 25; i++ {
		runID := fmt.Sprintf("run_soak_%02d", i)
		result, err := agent.Run(ctx, zenforge.Task{RunID: runID, Input: "complete one durable run"})
		if err != nil {
			t.Fatalf("run %s: %v", runID, err)
		}
		if result.Output != "soak complete" {
			t.Fatalf("run %s output = %q", runID, result.Output)
		}
		gotEvents, err := events.Read(ctx, runID, 0, 0)
		if err != nil {
			t.Fatalf("read events for %s: %v", runID, err)
		}
		if len(gotEvents) == 0 || gotEvents[0].Seq != 1 {
			t.Fatalf("run %s events not persisted with seqs: %#v", runID, gotEvents)
		}
		checkpoint, err := checkpoints.Load(ctx, runID)
		if err != nil {
			t.Fatalf("load checkpoint for %s: %v", runID, err)
		}
		if checkpoint.State.RunID != runID || string(checkpoint.State.Phase) != "completed" {
			t.Fatalf("run %s checkpoint = %#v", runID, checkpoint.State)
		}
	}

	summaries, err := checkpoints.List(ctx)
	if err != nil {
		t.Fatalf("list summaries: %v", err)
	}
	if len(summaries) != 25 {
		t.Fatalf("summary count = %d, want 25", len(summaries))
	}
}

func BenchmarkAgentRunStaticModel(b *testing.B) {
	agent := zenforge.New(zenforge.Config{
		Model:    staticModel{output: "ok"},
		MaxSteps: 2,
	})
	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := agent.Run(ctx, zenforge.Task{RunID: fmt.Sprintf("run_bench_%d", i), Input: "bench"}); err != nil {
			b.Fatal(err)
		}
	}
}

type staticModel struct {
	output string
}

func (m staticModel) Generate(ctx context.Context, req model.Request) (*model.Response, error) {
	return &model.Response{Message: model.Message{Role: "assistant", Content: m.output}}, ctx.Err()
}

func (m staticModel) Stream(ctx context.Context, req model.Request) (<-chan model.Event, error) {
	events := make(chan model.Event, 2)
	go func() {
		defer close(events)
		if err := ctx.Err(); err != nil {
			events <- model.Event{Type: model.EventError, Error: err}
			return
		}
		events <- model.Event{Type: model.EventDelta, Delta: m.output}
		events <- model.Event{Type: model.EventDone, Message: &model.Message{Role: "assistant", Content: m.output}}
	}()
	return events, nil
}
