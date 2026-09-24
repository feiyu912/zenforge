package modelstub_test

import (
	"context"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/examples/internal/modelstub"
	"github.com/feiyu912/zenforge/model/provider"
	"github.com/feiyu912/zenforge/tools"
)

type echoInput struct {
	Text string `json:"text" jsonschema:"required,description=Text to echo back"`
}

type echoOutput struct {
	Text string `json:"text"`
}

// TestScriptedEndpointDrivesARealAgentRun is the fixture's own contract: a real
// provider adapter built from the environment variables the examples use, a real
// agent loop, and a real tool call -- with only the model's words scripted. Every
// example test stands on this, so it is pinned here rather than in each of them.
func TestScriptedEndpointDrivesARealAgentRun(t *testing.T) {
	stub := modelstub.New(
		modelstub.Call("echo", map[string]any{"text": "hello from the tool"}),
		modelstub.Say("the tool said hello"),
	)
	defer stub.Close()

	for _, variable := range stub.Env() {
		name, value, _ := strings.Cut(variable, "=")
		t.Setenv(name, value)
	}
	model, err := provider.FromEnv()
	if err != nil {
		t.Fatalf("build the adapter from the stub's environment: %v", err)
	}

	echo := tools.Must("echo", "Echo text back unchanged.",
		func(context.Context, echoInput) (echoOutput, error) {
			return echoOutput{Text: "hello from the tool"}, nil
		})

	agent := zenforge.New(zenforge.Config{
		Model:        model,
		Instructions: "Call the echo tool, then answer.",
		Tools:        []zenforge.Tool{echo},
		MaxSteps:     4,
	})
	result, err := agent.Run(context.Background(), zenforge.Task{Input: "say hello"})
	if err != nil {
		t.Fatalf("run against the scripted endpoint: %v", err)
	}
	if result.Output != "the tool said hello" {
		t.Fatalf("output = %q", result.Output)
	}

	if calls := stub.Calls(); calls != 2 {
		t.Fatalf("the run made %d model calls, want 2", calls)
	}
	requests := stub.Requests()

	// The first call advertised the tool and carried the operator's words.
	if !requests[0].HasTool("echo") {
		t.Fatalf("the first request advertised %v, want echo", requests[0].ToolNames())
	}
	if !strings.Contains(requests[0].Text(), "say hello") {
		t.Fatalf("the first request did not carry the task: %q", requests[0].Text())
	}
	if requests[0].Delivered("echo") {
		t.Fatal("the first request already carried a tool result")
	}

	// The second call carried the tool's result, which is what proves the agent
	// ran the tool rather than only announcing that it would.
	if !requests[1].Delivered("echo") {
		t.Fatalf("the second request carried no echo result: %+v", requests[1].Messages)
	}
	if !strings.Contains(requests[1].Text(), "hello from the tool") {
		t.Fatalf("the tool's output is not in the second request: %q", requests[1].Text())
	}

	// Every request the stub served is a request the agent made, in order.
	if last, ok := stub.Last(); !ok || len(last.Messages) < 3 {
		t.Fatalf("last request has %d messages, want at least 3", len(last.Messages))
	}
}

// TestScriptedEndpointRepeatsItsLastTurn pins the boundary behaviour a test
// depends on: a run that asks for one step more than the script has gets an
// answer rather than an endpoint error, so a failure is always the example's.
func TestScriptedEndpointRepeatsItsLastTurn(t *testing.T) {
	stub := modelstub.New(modelstub.Say("only answer"))
	defer stub.Close()

	for _, variable := range stub.Env() {
		name, value, _ := strings.Cut(variable, "=")
		t.Setenv(name, value)
	}
	model, err := provider.FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	agent := zenforge.New(zenforge.Config{Model: model, Instructions: "Answer.", MaxSteps: 3})
	for _, input := range []string{"first", "second", "third"} {
		result, err := agent.Run(context.Background(), zenforge.Task{Input: input})
		if err != nil {
			t.Fatalf("run %q: %v", input, err)
		}
		if result.Output != "only answer" {
			t.Fatalf("run %q answered %q", input, result.Output)
		}
	}
	if calls := stub.Calls(); calls != 3 {
		t.Fatalf("the endpoint served %d calls, want 3", calls)
	}
}
