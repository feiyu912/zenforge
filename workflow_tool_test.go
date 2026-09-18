package zenforge

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	checkpointmemory "github.com/feiyu912/zenforge/checkpoint/memory"
	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/subagent"
	workflowtool "github.com/feiyu912/zenforge/tools/workflow"
	workflowengine "github.com/feiyu912/zenforge/workflow"
)

// workflowScriptModel returns a scripted model whose first turn asks for a
// workflow run and whose next turn is the parent's final answer.
func workflowScriptModel(t *testing.T, arguments string) *scriptedModel {
	t.Helper()
	return &scriptedModel{turns: []scriptedTurn{
		{events: []model.Event{{Message: &model.Message{ToolCalls: []model.ToolCallSpec{{
			ID:        "call_workflow",
			Name:      "workflow",
			Arguments: json.RawMessage(arguments),
		}}}}}},
		{events: []model.Event{{Delta: "parent final"}}},
	}}
}

// workflowtoolRequestForTest builds the decoded request the outcome renderer
// sees, without going through a model.
func workflowtoolRequestForTest() workflowtool.Request {
	return workflowtool.Request{
		Meta:   workflowengine.Meta{Name: "review", Description: "review the change"},
		Script: "return 1;",
	}
}

func TestAgentRunsWorkflowTool(t *testing.T) {
	checkpoints := checkpointmemory.New()
	fakeModel := workflowScriptModel(t, `{
		"meta": {"name": "review", "description": "Review the change in two steps", "phases": [{"title": "research"}]},
		"script": "phase(\"research\"); log(\"starting\"); const first = await agent(\"question one\"); const second = await agent(\"question two\", {schema: {type: \"object\", properties: {verdict: {type: \"string\"}}, required: [\"verdict\"], additionalProperties: false}}); return {first: first, verdict: second.verdict};"
	}`)
	var mu sync.Mutex
	var childRequests []subagent.Request
	runner := subagent.RunnerFunc(func(ctx context.Context, spec subagent.SubAgentSpec, task subagent.TaskSpec, req subagent.Request) (subagent.TaskResult, error) {
		mu.Lock()
		childRequests = append(childRequests, req)
		mu.Unlock()
		output := "answer one"
		if strings.Contains(task.Input, "question two") {
			output = "{\"verdict\": \"ok\"}"
		}
		return subagent.TaskResult{
			Output: output,
			Events: []subagent.Event{{Type: "child.note", Payload: map[string]any{"input": task.Input}}},
		}, nil
	})
	agent := New(Config{
		Model:            fakeModel,
		SubAgents:        SubAgentsEnabled,
		SubAgentRegistry: subagent.MustRegistry(subagent.SubAgentSpec{Name: "worker"}),
		SubAgentRunner:   runner,
		Checkpoints:      checkpoints,
	})

	events, err := agent.Stream(context.Background(), Task{RunID: "run_workflow", Input: "review it"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	var types []EventType
	var workflowEvents []Event
	var childEvents []Event
	for event := range events {
		types = append(types, event.Type)
		switch event.Type {
		case EventWorkflowPhase, EventWorkflowLog, EventWorkflowAgentStarted, EventWorkflowAgentDone:
			workflowEvents = append(workflowEvents, event)
		case EventSubtaskEvent:
			childEvents = append(childEvents, event)
		}
	}
	if !hasTool(fakeModel.requests[0].Tools, "workflow") {
		t.Fatalf("workflow tool was not advertised alongside the sub-agent runtime: %#v", fakeModel.requests[0].Tools)
	}
	assertContainsEvent(t, types, EventSubtaskStarted)
	assertContainsEvent(t, types, EventSubtaskEvent)
	assertContainsEvent(t, types, EventSubtaskDone)
	assertContainsEvent(t, types, EventToolResult)
	assertContainsEvent(t, types, EventWorkflowPhase)
	assertContainsEvent(t, types, EventWorkflowLog)
	assertContainsEvent(t, types, EventWorkflowAgentStarted)
	assertContainsEvent(t, types, EventWorkflowAgentDone)

	// The script's return value reaches the model as the reference's text
	// shape, and the parent's own turn still runs afterwards.
	second := fakeModel.requests[1]
	toolMessage := second.Messages[len(second.Messages)-1]
	if toolMessage.Role != "tool" || toolMessage.ToolCallID != "call_workflow" {
		t.Fatalf("missing workflow tool message: %#v", toolMessage)
	}
	if !contains(toolMessage.Content, `workflow "review" completed (2 agent(s)).`) {
		t.Fatalf("unexpected workflow summary: %q", toolMessage.Content)
	}
	if !contains(toolMessage.Content, `{"first":"answer one","verdict":"ok"}`) {
		t.Fatalf("workflow return value missing from %q", toolMessage.Content)
	}

	// A workflow's children belong to the script, not to the parent's plan:
	// the run state keeps no subtask entries, so resume replays the script.
	cp, err := checkpoints.Load(context.Background(), "run_workflow")
	if err != nil {
		t.Fatalf("Load checkpoint returned error: %v", err)
	}
	if len(cp.State.Subtasks) != 0 {
		t.Fatalf("workflow children leaked into run-state subtasks: %#v", cp.State.Subtasks)
	}

	if len(childRequests) != 2 {
		t.Fatalf("child runs = %d, want 2", len(childRequests))
	}
	for index, request := range childRequests {
		if request.RunID != "run_workflow" || request.ToolCallID != "call_workflow" || request.ParentTaskID != "call_workflow" {
			t.Fatalf("child %d lost its parent identity: %#v", index, request)
		}
		if len(request.Tasks) != 1 {
			t.Fatalf("child %d ran %d tasks, want 1", index, len(request.Tasks))
		}
		task := request.Tasks[0]
		if task.AgentName != "worker" {
			t.Fatalf("child %d agent = %q, want the only registered sub-agent", index, task.AgentName)
		}
		if task.Metadata["workflow.name"] != "review" || task.Metadata["workflow.parentToolCall"] != "call_workflow" {
			t.Fatalf("child %d metadata = %#v", index, task.Metadata)
		}
	}
	if childRequests[0].Tasks[0].ID == childRequests[1].Tasks[0].ID {
		t.Fatalf("workflow children shared an id: %q", childRequests[0].Tasks[0].ID)
	}
	if !strings.Contains(childRequests[1].Tasks[0].Input, "JSON Schema") {
		t.Fatalf("schema child was not asked for JSON: %q", childRequests[1].Tasks[0].Input)
	}
	if strings.Contains(childRequests[0].Tasks[0].Input, "JSON Schema") {
		t.Fatalf("text child was asked for JSON: %q", childRequests[0].Tasks[0].Input)
	}

	// The engine's own progress is first-class workflow events with the
	// workflow's identity, while a child's streamed events stay subtask events
	// carrying the child's own event type: the two views are separable.
	for _, event := range workflowEvents {
		if event.Payload["workflow"] != "review" || event.Payload["parentRunId"] != "run_workflow" ||
			event.Payload["toolCallId"] != "call_workflow" {
			t.Fatalf("%s lost the workflow identity: %#v", event.Type, event.Payload)
		}
	}
	if phase := firstEventOfType(workflowEvents, EventWorkflowPhase); phase == nil || phase.Payload["phase"] != "research" {
		t.Fatalf("phase event = %#v", phase)
	}
	if logEvent := firstEventOfType(workflowEvents, EventWorkflowLog); logEvent == nil || logEvent.Payload["message"] != "starting" {
		t.Fatalf("log event = %#v", logEvent)
	}
	var starts, dones int
	for _, event := range workflowEvents {
		switch event.Type {
		case EventWorkflowAgentStarted:
			starts++
			if event.Payload["seq"] == nil {
				t.Fatalf("agent.started lost its sequence: %#v", event.Payload)
			}
		case EventWorkflowAgentDone:
			dones++
			if stringValue(event.Payload["outcome"]) != "completed" {
				t.Fatalf("agent.done outcome = %#v", event.Payload)
			}
		}
	}
	// Both children are reported by the script's own bookkeeping, not only by
	// the subtask events their runs emit.
	if starts != 2 || dones != 2 {
		t.Fatalf("agent events = %d started, %d done", starts, dones)
	}
	var childKinds []string
	for _, event := range childEvents {
		childKinds = append(childKinds, stringValue(event.Payload["type"]))
	}
	if !contains(strings.Join(childKinds, ","), "child.note") {
		t.Fatalf("the child's own event was not forwarded: %v", childKinds)
	}
	if contains(strings.Join(childKinds, ","), "workflow.") {
		t.Fatalf("a workflow progress event still rides the subtask carrier: %v", childKinds)
	}
}

func firstEventOfType(events []Event, eventType EventType) *Event {
	for index := range events {
		if events[index].Type == eventType {
			return &events[index]
		}
	}
	return nil
}

func TestWorkflowToolReportsAFatalFailureToTheModel(t *testing.T) {
	fakeModel := workflowScriptModel(t, `{
		"meta": {"name": "over", "description": "Ask for more agents than allowed"},
		"script": "const first = await agent(\"one\"); const second = await agent(\"two\"); return [first, second];"
	}`)
	var mu sync.Mutex
	var starts int
	agent := New(Config{
		Model:            fakeModel,
		SubAgents:        SubAgentsEnabled,
		SubAgentRegistry: subagent.MustRegistry(subagent.SubAgentSpec{Name: "worker"}),
		SubAgentRunner: subagent.RunnerFunc(func(context.Context, subagent.SubAgentSpec, subagent.TaskSpec, subagent.Request) (subagent.TaskResult, error) {
			mu.Lock()
			starts++
			mu.Unlock()
			return subagent.TaskResult{Output: "answer"}, nil
		}),
		// The host's per-run budget: the first agent fits, the second trips
		// the backstop and fails the whole run.
		WorkflowLimits: workflowengine.Limits{MaxTotalAgents: 1},
	})

	events, err := agent.Stream(context.Background(), Task{RunID: "run_workflow_cap", Input: "cap it"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	for range events {
	}
	second := fakeModel.requests[1]
	toolMessage := second.Messages[len(second.Messages)-1]
	// The model sees the engine's own error text: it is the precise reason,
	// and the wrapper would only repeat what the code already says.
	if !contains(toolMessage.Content, "total agent cap") {
		t.Fatalf("unexpected failure text: %q", toolMessage.Content)
	}
	mu.Lock()
	defer mu.Unlock()
	if starts != 1 {
		t.Fatalf("a capped workflow started %d agents, want 1", starts)
	}
}

func TestWorkflowToolRefusesAModelOverrideWithoutAResolver(t *testing.T) {
	fakeModel := workflowScriptModel(t, `{
		"meta": {"name": "override", "description": "Ask for another model"},
		"script": "return await agent(\"one\", {model: \"gpt-5\"});"
	}`)
	agent := New(Config{
		Model:            fakeModel,
		SubAgents:        SubAgentsEnabled,
		SubAgentRegistry: subagent.MustRegistry(subagent.SubAgentSpec{Name: "worker"}),
		SubAgentRunner: subagent.RunnerFunc(func(context.Context, subagent.SubAgentSpec, subagent.TaskSpec, subagent.Request) (subagent.TaskResult, error) {
			t.Fatal("a refused override must not start a child")
			return subagent.TaskResult{}, nil
		}),
	})
	events, err := agent.Stream(context.Background(), Task{RunID: "run_workflow_override", Input: "override"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	for range events {
	}
	second := fakeModel.requests[1]
	toolMessage := second.Messages[len(second.Messages)-1]
	if !contains(toolMessage.Content, "cannot resolve a model by name") {
		t.Fatalf("unexpected refusal text: %q", toolMessage.Content)
	}
}

func TestWorkflowToolNeedsAConfiguredSubAgent(t *testing.T) {
	agent := New(Config{SubAgents: SubAgentsEnabled})
	if _, err := agent.workflowAgentName(); err == nil {
		t.Fatal("expected an error without a configured sub-agent")
	}
	agent = New(Config{
		SubAgents:     SubAgentsEnabled,
		WorkflowAgent: "named",
		SubAgentSpecs: []subagent.SubAgentSpec{{Name: "worker"}},
	})
	name, err := agent.workflowAgentName()
	if err != nil || name != "named" {
		t.Fatalf("workflowAgentName = %q, %v", name, err)
	}
	agent = New(Config{
		SubAgents:     SubAgentsEnabled,
		SubAgentSpecs: []subagent.SubAgentSpec{{Name: "worker"}},
	})
	name, err = agent.workflowAgentName()
	if err != nil || name != "worker" {
		t.Fatalf("workflowAgentName = %q, %v", name, err)
	}
}

func TestWorkflowStructuredOutput(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
		ok    bool
	}{
		{name: "plain", input: `{"verdict":"ok"}`, want: `{"verdict":"ok"}`, ok: true},
		{name: "whitespace", input: "  \n {\"verdict\": \"ok\"} \n ", want: `{"verdict":"ok"}`, ok: true},
		{name: "fenced", input: "```json\n{\"verdict\": \"ok\"}\n```", want: `{"verdict":"ok"}`, ok: true},
		{name: "prose", input: "Here it is: {\"verdict\": \"ok\"} — hope that helps", want: `{"verdict":"ok"}`, ok: true},
		{name: "not-json", input: "I could not answer", ok: false},
		{name: "array-not-object", input: `["ok"]`, ok: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := workflowStructuredOutput(testCase.input)
			if ok != testCase.ok {
				t.Fatalf("ok = %v, want %v (value %s)", ok, testCase.ok, got)
			}
			if ok && string(got) != testCase.want {
				t.Fatalf("value = %s, want %s", got, testCase.want)
			}
		})
	}
}

func TestWorkflowToolOutcomeRendersBothPaths(t *testing.T) {
	request := workflowtoolRequestForTest()
	result, err := workflowToolOutcome(request, workflowengine.Result{
		Value:         json.RawMessage(`{"verdict":"ok"}`),
		StopReason:    workflowengine.StopCompleted,
		AgentsStarted: 2,
	}, nil)
	if err != nil {
		t.Fatalf("completed run returned error: %v", err)
	}
	if result.ExitCode != 0 || result.Error != "" {
		t.Fatalf("completed run result = %#v", result)
	}
	if result.Structured["agentsStarted"] != 2 || result.Structured["stopReason"] != string(workflowengine.StopCompleted) {
		t.Fatalf("completed structured result = %#v", result.Structured)
	}

	failure := &workflowengine.Error{Code: workflowengine.CodeAgentCap, Message: "too many agents"}
	result, err = workflowToolOutcome(request, workflowengine.Result{
		StopReason:    workflowengine.StopError,
		AgentsStarted: 1,
	}, failure)
	if err == nil || result.ExitCode != 1 {
		t.Fatalf("failed run result = %#v, err = %v", result, err)
	}
	if result.Structured["code"] != string(workflowengine.CodeAgentCap) {
		t.Fatalf("failed structured result = %#v", result.Structured)
	}
	if !contains(result.Output, "did not complete") || !contains(result.Output, "too many agents") {
		t.Fatalf("failed output = %q", result.Output)
	}
}
