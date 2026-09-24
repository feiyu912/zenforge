package scripted

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// chatMessage builds a wire message for a test request.
func chatMessage(role, content string) map[string]any {
	return map[string]any{"role": role, "content": content}
}

// issueCall builds an assistant message that requested a tool call.
func issueCall(id, name, arguments string) map[string]any {
	return map[string]any{
		"role":    "assistant",
		"content": "",
		"tool_calls": []any{map[string]any{
			"id":       id,
			"type":     "function",
			"function": map[string]any{"name": name, "arguments": arguments},
		}},
	}
}

// resultMessage builds a tool message answering a call.
func resultMessage(id, content string) map[string]any {
	return map[string]any{"role": "tool", "tool_call_id": id, "content": content}
}

// completion is the decoded non-streaming response a test asserts on.
type completion struct {
	Choices []struct {
		Message struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// postJSON sends one request body and returns the raw response.
func postJSON(t *testing.T, url string, payload map[string]any) ([]byte, int) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	response, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return data, response.StatusCode
}

// nonStream posts a non-streaming request and decodes the completion.
func nonStream(t *testing.T, url string, messages []map[string]any) completion {
	t.Helper()
	data, status := postJSON(t, url, map[string]any{
		"model":    "scripted-model",
		"stream":   false,
		"messages": messages,
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{
				"name":       "read_file",
				"parameters": map[string]any{"type": "object"},
			}},
			map[string]any{"type": "function", "function": map[string]any{
				"name":       "write_file",
				"parameters": map[string]any{"type": "object"},
			}},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", status, data)
	}
	var decoded completion
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode completion: %v", err)
	}
	return decoded
}

func turnToolName(t *testing.T, completion completion) (string, string) {
	t.Helper()
	if len(completion.Choices) == 0 {
		t.Fatalf("completion has no choices")
	}
	calls := completion.Choices[0].Message.ToolCalls
	if len(calls) != 1 {
		t.Fatalf("completion has %d tool calls, want 1", len(calls))
	}
	return calls[0].ID, calls[0].Function.Name
}

func testScript() []Turn {
	return []Turn{
		{ToolCalls: []ToolCall{{Name: "read_file", Arguments: json.RawMessage(`{"path":"input.txt"}`)}}},
		{ToolCalls: []ToolCall{{Name: "write_file", Arguments: json.RawMessage(`{"path":"out.txt","content":"x"}`)}}},
		{Content: "done"},
	}
}

// TestTurnSelectionAdvancesOnlyOnIssuedToolResults is the contract's rule: a
// request carrying a tool result for an id the endpoint issued advances the
// script; any other request re-serves the last turn, and the final turn repeats
// forever once the script is exhausted.
func TestTurnSelectionAdvancesOnlyOnIssuedToolResults(t *testing.T) {
	server := MustNew(testScript())
	defer server.Close()
	url := server.BaseURL() + "/chat/completions"

	first := nonStream(t, url, []map[string]any{chatMessage("user", "go")})
	id, name := turnToolName(t, first)
	if id != "call_0_0" || name != "read_file" {
		t.Fatalf("first call = (%s, %s), want (call_0_0, read_file)", id, name)
	}

	// The same question asked again gets the same answer: the script must not
	// advance on a request that carries no tool result.
	repeat := nonStream(t, url, []map[string]any{chatMessage("user", "go")})
	repeatID, repeatName := turnToolName(t, repeat)
	if repeatID != id || repeatName != name {
		t.Fatalf("re-served call = (%s, %s), want the same (%s, %s)", repeatID, repeatName, id, name)
	}

	// The result for the id the endpoint issued advances to the next turn.
	advanced := nonStream(t, url, []map[string]any{
		chatMessage("user", "go"),
		issueCall("call_0_0", "read_file", `{"path":"input.txt"}`),
		resultMessage("call_0_0", "hello"),
	})
	writeID, writeName := turnToolName(t, advanced)
	if writeID != "call_1_0" || writeName != "write_file" {
		t.Fatalf("advanced call = (%s, %s), want (call_1_0, write_file)", writeID, writeName)
	}

	// A tool result for an id the endpoint never issued does not advance: it
	// re-serves the turn it served last.
	unknown := nonStream(t, url, []map[string]any{
		chatMessage("user", "go"),
		resultMessage("call_999_9", "not ours"),
	})
	unknownID, unknownName := turnToolName(t, unknown)
	if unknownID != writeID || unknownName != writeName {
		t.Fatalf("unknown result served (%s, %s), want the re-served (%s, %s)", unknownID, unknownName, writeID, writeName)
	}

	// The next turn is served once its own call's result comes back.
	final := nonStream(t, url, []map[string]any{
		chatMessage("user", "go"),
		issueCall("call_1_0", "write_file", `{"path":"out.txt","content":"x"}`),
		resultMessage("call_1_0", "written"),
	})
	if got := final.Choices[0].Message.Content; got != "done" {
		t.Fatalf("final content = %q, want %q", got, "done")
	}
	if got := final.Choices[0].FinishReason; got != "stop" {
		t.Fatalf("final finish reason = %q, want stop", got)
	}

	// An exhausted script repeats its last turn.
	after := nonStream(t, url, []map[string]any{chatMessage("user", "go")})
	if got := after.Choices[0].Message.Content; got != "done" {
		t.Fatalf("exhausted script served %q, want the last turn %q", got, "done")
	}

	requests := server.Requests()
	if len(requests) != 6 {
		t.Fatalf("recorded %d requests, want 6", len(requests))
	}
	wantServed := []int{0, 0, 1, 1, 2, 2}
	for index, want := range wantServed {
		if requests[index].ServedTurn != want {
			t.Fatalf("request %d served turn %d, want %d", index, requests[index].ServedTurn, want)
		}
	}
	if server.Count() != 6 {
		t.Fatalf("Count() = %d, want 6", server.Count())
	}
	if server.PromptBytes() <= 0 {
		t.Fatalf("PromptBytes() = %d, want > 0", server.PromptBytes())
	}
	if server.ToolSchemaBytes() <= 0 || server.ToolSchemaBytes() >= server.PromptBytes() {
		t.Fatalf("ToolSchemaBytes() = %d, want a nonzero strict subset of %d", server.ToolSchemaBytes(), server.PromptBytes())
	}
	if requests[0].ToolSchemaBytes <= 0 {
		t.Fatalf("request ToolSchemaBytes = %d, want > 0", requests[0].ToolSchemaBytes)
	}
	if len(requests[0].Tools) != 2 || requests[0].Tools[0].Name != "read_file" {
		t.Fatalf("recorded tools = %+v, want read_file and write_file", requests[0].Tools)
	}
	if !requests[0].HasTool("write_file") {
		t.Fatalf("HasTool(write_file) = false")
	}
}

// TestStreamingServesSSEWithUsage covers the streaming shape: deltas, a
// finish_reason, a usage chunk when the client asked for one, and [DONE].
func TestStreamingServesSSEWithUsage(t *testing.T) {
	server := MustNew(testScript())
	defer server.Close()

	body, status := postJSON(t, server.BaseURL()+"/chat/completions", map[string]any{
		"model":          "scripted-model",
		"stream":         true,
		"messages":       []any{chatMessage("user", "go")},
		"stream_options": map[string]any{"include_usage": true},
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	text := string(body)
	for _, want := range []string{`"tool_calls"`, `"call_0_0"`, `"finish_reason":"tool_calls"`, `"usage"`, "[DONE]"} {
		if !strings.Contains(text, want) {
			t.Fatalf("stream body does not contain %s:\n%s", want, text)
		}
	}
	// Every data line must be a JSON object or the sentinel, because a client
	// that sees a malformed chunk fails for a reason the benchmark is not about.
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("stream chunk is not JSON: %v\n%s", err, payload)
		}
	}
	if server.ModelServiceTime() < 0 {
		t.Fatalf("ModelServiceTime() = %v, want >= 0", server.ModelServiceTime())
	}
}

// TestStreamOmitsUsageUnlessAsked keeps the endpoint faithful to a real
// provider: a streaming client that did not ask for usage does not get an empty
// choices chunk that a strict parser could choke on.
func TestStreamOmitsUsageUnlessAsked(t *testing.T) {
	server := MustNew(testScript())
	defer server.Close()
	body, status := postJSON(t, server.BaseURL()+"/chat/completions", map[string]any{
		"model":    "scripted-model",
		"stream":   true,
		"messages": []any{chatMessage("user", "go")},
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if strings.Contains(string(body), `"usage"`) {
		t.Fatalf("stream body contains usage without include_usage:\n%s", body)
	}
}

// TestNonStreamingAlwaysReportsUsage covers the other half of the contract:
// the non-streaming shape always carries usage, computed trivially from bytes.
func TestNonStreamingAlwaysReportsUsage(t *testing.T) {
	server := MustNew(testScript())
	defer server.Close()
	decoded := nonStream(t, server.BaseURL()+"/chat/completions", []map[string]any{chatMessage("user", "go")})
	if decoded.Usage.PromptTokens <= 0 {
		t.Fatalf("prompt_tokens = %d, want > 0", decoded.Usage.PromptTokens)
	}
	if decoded.Usage.CompletionTokens <= 0 {
		t.Fatalf("completion_tokens = %d, want > 0", decoded.Usage.CompletionTokens)
	}
}

// TestSnapshotsAreCopiesAndConcurrencySafe drives the endpoint from many
// goroutines and reads snapshots while they run. Under -race this catches the
// shared-slice and shared-map bugs a benchmark endpoint is prone to.
func TestSnapshotsAreCopiesAndConcurrencySafe(t *testing.T) {
	server := MustNew(testScript())
	defer server.Close()
	url := server.BaseURL() + "/chat/completions"

	const callers = 8
	const perCaller = 4
	var wg sync.WaitGroup
	for caller := 0; caller < callers; caller++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := 0; index < perCaller; index++ {
				payload, err := json.Marshal(map[string]any{
					"model":    "scripted-model",
					"stream":   false,
					"messages": []any{chatMessage("user", "go")},
				})
				if err != nil {
					continue
				}
				response, err := http.Post(url, "application/json", bytes.NewReader(payload))
				if err != nil {
					continue
				}
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
				_ = server.Requests()
				_ = server.Count()
				_ = server.PromptBytes()
				_ = server.ToolSchemaBytes()
				_ = server.ModelServiceTime()
			}
		}()
	}
	wg.Wait()

	if got := server.Count(); got != callers*perCaller {
		t.Fatalf("Count() = %d, want %d", got, callers*perCaller)
	}
	// The snapshot must not alias the server's records: mutating it cannot
	// change a later snapshot.
	snapshot := server.Requests()
	for index := range snapshot {
		snapshot[index].ServedTurn = -1
		snapshot[index].Messages = nil
		snapshot[index].Body[0] = 0
	}
	fresh := server.Requests()
	if fresh[0].ServedTurn == -1 || len(fresh[0].Messages) == 0 || fresh[0].Body[0] == 0 {
		t.Fatalf("Requests() returned aliased records: %+v", fresh[0])
	}
}

// TestEndpointRejectsOtherPaths keeps the endpoint honest about being an
// OpenAI-compatible /chat/completions server and nothing else.
func TestEndpointRejectsOtherPaths(t *testing.T) {
	server := MustNew(testScript())
	defer server.Close()
	response, err := http.Get(server.BaseURL() + "/models")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.StatusCode)
	}
}

// TestNewRejectsEmptyScript: an endpoint that can never answer would fail every
// runner for a reason the runner is not responsible for.
func TestNewRejectsEmptyScript(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatalf("New(nil) = nil error, want a rejection")
	}
	if _, err := New([]Turn{{}}); err == nil {
		t.Fatalf("New(empty turn) = nil error, want a rejection")
	}
}

// TestParseScriptValidates is the frozen-script loader's contract.
func TestParseScriptValidates(t *testing.T) {
	valid := []byte(`{"task":"t","turns":[{"tool_calls":[{"name":"read_file","arguments":{"path":"a"}}]}]}`)
	script, err := ParseScript(valid)
	if err != nil {
		t.Fatalf("ParseScript(valid) error = %v", err)
	}
	if script.Task != "t" || len(script.Turns) != 1 || script.Turns[0].ToolCalls[0].Name != "read_file" {
		t.Fatalf("ParseScript(valid) = %+v", script)
	}
	for name, input := range map[string]string{
		"no task":       `{"turns":[{"content":"x"}]}`,
		"no turns":      `{"task":"t","turns":[]}`,
		"empty turn":    `{"task":"t","turns":[{}]}`,
		"nameless call": `{"task":"t","turns":[{"tool_calls":[{"arguments":{}}]}]}`,
		"unknown field": `{"task":"t","turns":[{"content":"x"}],"extra":1}`,
	} {
		if _, err := ParseScript([]byte(input)); err == nil {
			t.Fatalf("ParseScript(%s) = nil error, want a rejection", name)
		}
	}
}

func TestBaseURLAndRequestHelpers(t *testing.T) {
	server := MustNew(testScript())
	defer server.Close()
	if !strings.HasSuffix(server.BaseURL(), "/v1") {
		t.Fatalf("BaseURL() = %q, want a /v1 suffix", server.BaseURL())
	}
	_ = nonStream(t, server.BaseURL()+"/chat/completions", []map[string]any{chatMessage("user", "go")})
	request := server.Requests()[0]
	last, ok := request.LastMessage()
	if !ok || last.Role != "user" {
		t.Fatalf("LastMessage() = (%+v, %v), want the user message", last, ok)
	}
	if got := fmt.Sprint(request.ServedTurn); got != "0" {
		t.Fatalf("ServedTurn = %s, want 0", got)
	}
}
