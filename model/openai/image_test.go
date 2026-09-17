package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/model"
)

func TestChatRequestSendsImagesAsMultipartContent(t *testing.T) {
	client := &Client{model: "gpt-4.1"}
	request := client.chatRequest(model.Request{Messages: []model.Message{
		{Role: "user", Content: "what is this?", Images: []model.Image{
			{MediaType: "image/png", Data: []byte{1, 2, 3}, Path: "a.png", Detail: "high"},
		}},
		{Role: "user", Content: "plain text"},
	}})
	encoded, err := json.Marshal(request.Messages)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	var messages []map[string]any
	if err := json.Unmarshal(encoded, &messages); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	parts, ok := messages[0]["content"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("content = %#v", messages[0]["content"])
	}
	text := parts[0].(map[string]any)
	if text["type"] != "text" || text["text"] != "what is this?" {
		t.Fatalf("text part = %#v", text)
	}
	imagePart := parts[1].(map[string]any)
	if imagePart["type"] != "image_url" {
		t.Fatalf("image part = %#v", imagePart)
	}
	imageURL := imagePart["image_url"].(map[string]any)
	if imageURL["url"] != "data:image/png;base64,AQID" || imageURL["detail"] != "high" {
		t.Fatalf("image_url = %#v", imageURL)
	}
	// A text-only message keeps the plain string form the API expects.
	if messages[1]["content"] != "plain text" {
		t.Fatalf("plain content = %#v", messages[1]["content"])
	}
}

func TestReasoningDeltasAreSeparateFromText(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return sseResponse(
			"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"reasoning_content\":\"think \"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"harder\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"content\":\"answer\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
				"data: [DONE]\n\n",
		), nil
	})}
	client := New(Config{BaseURL: "https://example.test/v1", Model: "gpt-test", HTTPClient: httpClient})
	events, err := client.Stream(context.Background(), model.Request{Messages: []model.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	var reasoning, text strings.Builder
	var final model.Message
	for event := range events {
		switch event.Type {
		case model.EventReasoning:
			reasoning.WriteString(event.Delta)
		case model.EventDelta:
			text.WriteString(event.Delta)
		case model.EventDone:
			if event.Message != nil {
				final = *event.Message
			}
		}
	}
	if reasoning.String() != "think harder" {
		t.Fatalf("reasoning = %q", reasoning.String())
	}
	if text.String() != "answer" {
		t.Fatalf("text = %q", text.String())
	}
	// The final message keeps reasoning out of Content: it is not answer
	// text, and a caller must be able to tell them apart.
	if final.Reasoning != "think harder" || final.Content != "answer" {
		t.Fatalf("final = %#v", final)
	}
}
