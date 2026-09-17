package anthropic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/model"
)

func TestRequestSendsImagesAndReplaysThinking(t *testing.T) {
	var got messagesRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("Decode request returned error: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(
			"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n" +
				"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n" +
				"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
				"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
				"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
				"data: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()
	client := New(Config{BaseURL: server.URL, Model: "claude-test"})
	events, err := client.Stream(context.Background(), model.Request{Messages: []model.Message{
		{Role: "user", Content: "describe this", Images: []model.Image{{MediaType: "image/png", Data: []byte{1, 2, 3}}}},
		{Role: "assistant", Content: "I see a diagram", Reasoning: "the image shows boxes", ReasoningSignature: "sig-1"},
	}})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	for range events {
	}
	if len(got.Messages) != 2 {
		t.Fatalf("messages = %#v", got.Messages)
	}
	// The user turn carries the image as a base64 source block after the
	// text.
	userBlocks := got.Messages[0].Content
	if len(userBlocks) != 2 || userBlocks[0].Type != "text" || userBlocks[1].Type != "image" {
		t.Fatalf("user blocks = %#v", userBlocks)
	}
	if userBlocks[1].Source == nil || userBlocks[1].Source.Type != "base64" || userBlocks[1].Source.MediaType != "image/png" || userBlocks[1].Source.Data != "AQID" {
		t.Fatalf("image source = %#v", userBlocks[1].Source)
	}
	// The assistant turn replays the thinking block first, verbatim and
	// with its signature: the API rejects a thinking block without it.
	assistantBlocks := got.Messages[1].Content
	if len(assistantBlocks) != 2 {
		t.Fatalf("assistant blocks = %#v", assistantBlocks)
	}
	if assistantBlocks[0].Type != "thinking" || assistantBlocks[0].Text != "the image shows boxes" || assistantBlocks[0].Signature != "sig-1" {
		t.Fatalf("thinking block = %#v", assistantBlocks[0])
	}
	if assistantBlocks[1].Type != "text" || assistantBlocks[1].Text != "I see a diagram" {
		t.Fatalf("text block = %#v", assistantBlocks[1])
	}
	// Reasoning without a signature is not replayed: a half block would be
	// rejected, and the transcript still keeps the text.
	unsignedRequest, err := client.messagesRequest(model.Request{Messages: []model.Message{
		{Role: "assistant", Content: "answer", Reasoning: "no signature"},
	}})
	if err != nil {
		t.Fatalf("messagesRequest returned error: %v", err)
	}
	if len(unsignedRequest.Messages) != 1 || len(unsignedRequest.Messages[0].Content) != 1 || unsignedRequest.Messages[0].Content[0].Type != "text" {
		t.Fatalf("unsigned blocks = %#v", unsignedRequest.Messages[0].Content)
	}
}

func TestRequestRefusesUnsupportedImageMediaType(t *testing.T) {
	client := New(Config{BaseURL: "https://example.test", Model: "claude-test"})
	if _, err := client.messagesRequest(model.Request{Messages: []model.Message{
		{Role: "user", Content: "see this", Images: []model.Image{{MediaType: "image/tiff", Data: []byte{1}}}},
	}}); err == nil || !strings.Contains(err.Error(), "image/tiff") {
		t.Fatalf("error = %v", err)
	}
	// An image with no media type is refused too, rather than sent as a
	// broken source block.
	if _, err := client.messagesRequest(model.Request{Messages: []model.Message{
		{Role: "user", Images: []model.Image{{Data: []byte{1}}}},
	}}); err == nil {
		t.Fatal("an image with no media type was accepted")
	}
}

func TestStreamCapturesThinkingBlocks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(
			"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n" +
				"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\"}}\n\n" +
				"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"let me \"}}\n\n" +
				"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"look\"}}\n\n" +
				"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"sig-9\"}}\n\n" +
				"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
				"data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\"}}\n\n" +
				"data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}\n\n" +
				"data: {\"type\":\"content_block_stop\",\"index\":1}\n\n" +
				"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n" +
				"data: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()
	client := New(Config{BaseURL: server.URL, Model: "claude-test"})
	events, err := client.Stream(context.Background(), model.Request{Messages: []model.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	var reasoning strings.Builder
	var final model.Message
	for event := range events {
		if event.Type == model.EventReasoning {
			reasoning.WriteString(event.Delta)
		}
		if event.Type == model.EventDone && event.Message != nil {
			final = *event.Message
		}
	}
	if reasoning.String() != "let me look" {
		t.Fatalf("reasoning deltas = %q", reasoning.String())
	}
	if final.Content != "done" || final.Reasoning != "let me look" || final.ReasoningSignature != "sig-9" {
		t.Fatalf("final = %#v", final)
	}
}
