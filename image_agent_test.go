package zenforge

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	checkpointmemory "github.com/feiyu912/zenforge/checkpoint/memory"
	"github.com/feiyu912/zenforge/harness"
	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/tool"
	"github.com/feiyu912/zenforge/tools"
	"github.com/feiyu912/zenforge/tools/viewimage"
	"github.com/feiyu912/zenforge/workspace/local"
)

// imageToolResult is a stand-in tool that attaches an image to its result,
// as view_image does.
func imageToolResult(t *testing.T, name string) tool.Tool {
	t.Helper()
	return tools.Must(name, "show an image", func(context.Context, struct{}) (viewimage.Output, error) {
		img := image.NewRGBA(image.Rect(0, 0, 1, 1))
		img.Set(0, 0, color.RGBA{B: 255, A: 255})
		var buffer bytes.Buffer
		if err := png.Encode(&buffer, img); err != nil {
			return viewimage.Output{}, err
		}
		return viewimage.Output{
			Path:      "shot.png",
			MediaType: "image/png",
			Bytes:     buffer.Len(),
			Note:      "showing shot.png",
			Images:    []model.Image{{MediaType: "image/png", Data: buffer.Bytes(), Path: "shot.png"}},
		}, nil
	})
}

func TestToolImageIsReplayedOnTheNextModelCall(t *testing.T) {
	ctx := context.Background()
	fake := &scriptedModel{turns: []scriptedTurn{
		// First turn: call the image tool.
		{events: []model.Event{{ToolCalls: []model.ToolCallSpec{{ID: "call_1", Name: "show_image", Arguments: json.RawMessage(`{}`)}}}}},
		// Second turn: answer, once both messages are in the transcript.
		{events: []model.Event{{Delta: "I can see it"}}},
	}}
	agent := New(Config{
		Model:       fake,
		Events:      &testEventStore{},
		Checkpoints: checkpointmemory.New(),
		WorkingDir:  t.TempDir(),
		MaxSteps:    4,
		Tools:       []tool.Tool{imageToolResult(t, "show_image")},
	})
	stream, err := agent.Stream(ctx, Task{Input: "look at the screenshot"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	if len(eventsByType(collected, EventRunDone)) != 1 {
		t.Fatalf("run did not complete: %v", collected)
	}
	if len(fake.requests) != 2 {
		t.Fatalf("model requests = %d", len(fake.requests))
	}
	// The second request carries the image on the tool message, which is
	// the whole point: a tool result alone would be text the model cannot
	// see.
	var found bool
	for _, message := range fake.requests[1].Messages {
		if message.Role == "tool" && len(message.Images) == 1 {
			found = true
			if message.Images[0].MediaType != "image/png" || len(message.Images[0].Data) == 0 {
				t.Fatalf("image = %#v", message.Images[0])
			}
		}
	}
	if !found {
		t.Fatalf("the image never reached the model: %#v", fake.requests[1].Messages)
	}
}

func TestImageSurvivesCheckpointRoundTrip(t *testing.T) {
	ctx := context.Background()
	// The transcript is durable: an image attached to a message must still
	// be there after a restart, or a resumed run would ask the model about
	// a picture it can no longer see. The checkpoint is JSON, which is
	// exactly the round trip that could lose []byte.
	fake := &scriptedModel{turns: []scriptedTurn{
		{events: []model.Event{{ToolCalls: []model.ToolCallSpec{{ID: "call_1", Name: "show_image", Arguments: json.RawMessage(`{}`)}}}}},
		{events: []model.Event{{Delta: "I can see it"}}},
	}}
	store := checkpointmemory.New()
	agent := New(Config{
		Model:       fake,
		Events:      &testEventStore{},
		Checkpoints: store,
		WorkingDir:  t.TempDir(),
		MaxSteps:    4,
		Tools:       []tool.Tool{imageToolResult(t, "show_image")},
	})
	stream, err := agent.Stream(ctx, Task{RunID: "run_image", Input: "look"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collectRunEvents(t, stream)

	checkpoint, err := store.Load(ctx, "run_image")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if checkpoint == nil {
		t.Fatal("no checkpoint was written")
	}
	// The checkpoint round trip is JSON, which is exactly what could lose
	// []byte; converting it back to model messages must carry the image.
	reloaded := checkpoint.State
	if err := harness.ValidateRunState(reloaded); err != nil {
		t.Fatalf("the reloaded state is invalid: %v", err)
	}
	var found bool
	for _, message := range reloaded.Messages {
		if message.Role != "tool" {
			continue
		}
		images := messageImages(message)
		if len(images) != 1 {
			continue
		}
		found = true
		if images[0].MediaType != "image/png" || len(images[0].Data) == 0 {
			t.Fatalf("image = %#v", images[0])
		}
	}
	if !found {
		t.Fatalf("the image was lost in the checkpoint: %#v", reloaded.Messages)
	}
	// And the model-facing conversion still carries it.
	var replayed bool
	for _, message := range agent.modelMessages(reloaded) {
		if len(message.Images) == 1 {
			replayed = true
		}
	}
	if !replayed {
		t.Fatal("the reloaded state did not replay the image")
	}
}

func TestReasoningIsCapturedAndReplayed(t *testing.T) {
	ctx := context.Background()
	fake := &scriptedModel{turns: []scriptedTurn{
		// The first turn reasons and calls a tool.
		{events: []model.Event{
			{Type: model.EventReasoning, Delta: "let me think "},
			{Type: model.EventReasoning, Delta: "about this"},
			{ToolCalls: []model.ToolCallSpec{{ID: "call_1", Name: "echo", Arguments: json.RawMessage(`{}`)}}},
			{Message: &model.Message{Role: "assistant", Reasoning: "let me think about this", ReasoningSignature: "sig-1"}},
		}},
		{events: []model.Event{{Delta: "done"}}},
	}}
	agent := New(Config{
		Model:       fake,
		Events:      &testEventStore{},
		Checkpoints: checkpointmemory.New(),
		WorkingDir:  t.TempDir(),
		MaxSteps:    4,
		Tools:       []tool.Tool{echoTool{}},
	})
	stream, err := agent.Stream(ctx, Task{Input: "work"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	collected := collectRunEvents(t, stream)
	if len(eventsByType(collected, EventRunDone)) != 1 {
		t.Fatalf("run did not complete: %v", collected)
	}
	// Reasoning streams on its own event and never as answer text.
	reasoning := eventsByType(collected, EventModelReasoning)
	if len(reasoning) != 2 {
		t.Fatalf("reasoning events = %#v", reasoning)
	}
	if deltas := eventsByType(collected, EventModelDelta); len(deltas) != 1 || stringValue(deltas[0].Payload["textDelta"]) != "done" {
		t.Fatalf("answer deltas = %#v", deltas)
	}
	// The next request replays the reasoning with its signature, which is
	// what a provider that requires signed thinking blocks needs.
	var replayed bool
	for _, message := range fake.requests[1].Messages {
		if message.Role == "assistant" && message.Reasoning == "let me think about this" {
			replayed = true
			if message.ReasoningSignature != "sig-1" {
				t.Fatalf("signature = %q", message.ReasoningSignature)
			}
		}
	}
	if !replayed {
		t.Fatalf("reasoning was not replayed: %#v", fake.requests[1].Messages)
	}
}

func TestViewImageToolRidesOnTheAgentWorkspace(t *testing.T) {
	// The tool the CLI registers reads through the workspace, so the same
	// confinement rules as the file tools apply.
	root := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatalf("png.Encode returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "shot.png"), buffer.Bytes(), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	ws, err := local.New(local.Config{Root: root, AllowBinaryRead: true, MaxReadBytes: 1 << 20})
	if err != nil {
		t.Fatalf("local.New returned error: %v", err)
	}
	viewer, err := viewimage.New(viewimage.Config{Workspace: ws})
	if err != nil {
		t.Fatalf("viewimage.New returned error: %v", err)
	}
	result, err := viewer.Call(context.Background(), json.RawMessage(`{"path":"shot.png"}`), tool.Context{})
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if images := imagesFromMeta(result.Meta); len(images) != 1 {
		t.Fatalf("images = %#v (meta %#v)", images, result.Meta)
	}
	// An escape is refused by the workspace, not by the tool's own logic.
	if _, err := viewer.Call(context.Background(), json.RawMessage(`{"path":"../shot.png"}`), tool.Context{}); err == nil {
		t.Fatal("an escaping path was accepted")
	}
}
