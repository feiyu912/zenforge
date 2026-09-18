package workflow

import (
	"encoding/json"
	"errors"
	"testing"

	enginetool "github.com/feiyu912/zenforge/tool"
)

func TestDecodeAcceptsAWorkflowCall(t *testing.T) {
	request, err := Decode(json.RawMessage(`{
		"meta": {"name": "review", "description": "Review it", "whenToUse": "before merging", "phases": [{"title": "scan", "detail": "read the diff"}]},
		"script": "return await agent(\"go\");",
		"args": {"paths": ["a.go"]}
	}`))
	if err != nil {
		t.Fatalf("Decode returned error: %v", err)
	}
	if request.Meta.Name != "review" || len(request.Meta.Phases) != 1 || request.Meta.Phases[0].Title != "scan" {
		t.Fatalf("decoded meta = %#v", request.Meta)
	}
	engineRequest := request.Engine()
	if engineRequest.Script != request.Script || string(engineRequest.Args) != `{"paths": ["a.go"]}` {
		t.Fatalf("engine request = %#v", engineRequest)
	}
}

func TestDecodeRefusesMalformedCalls(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{name: "empty", raw: ""},
		{name: "not-json", raw: "{"},
		{name: "unknown-field", raw: `{"meta":{"name":"n","description":"d"},"script":"return 1;","extra":true}`},
		{name: "unknown-meta-field", raw: `{"meta":{"name":"n","description":"d","owner":"me"},"script":"return 1;"}`},
		{name: "missing-script", raw: `{"meta":{"name":"n","description":"d"}}`},
		{name: "blank-script", raw: `{"meta":{"name":"n","description":"d"},"script":"   "}`},
		{name: "missing-name", raw: `{"meta":{"description":"d"},"script":"return 1;"}`},
		{name: "missing-description", raw: `{"meta":{"name":"n"},"script":"return 1;"}`},
		{name: "phase-without-title", raw: `{"meta":{"name":"n","description":"d","phases":[{"detail":"x"}]},"script":"return 1;"}`},
		{name: "args-not-an-object", raw: `{"meta":{"name":"n","description":"d"},"script":"return 1;","args":[1,2]}`},
		{name: "multiple-values", raw: `{"meta":{"name":"n","description":"d"},"script":"return 1;"} {}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := Decode(json.RawMessage(testCase.raw))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !errors.Is(err, enginetool.ErrInvalidArguments) {
				t.Fatalf("error = %v, want ErrInvalidArguments", err)
			}
		})
	}
}

func TestToolDescribesItselfAndRefusesDirectCalls(t *testing.T) {
	tool, err := New()
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if tool.Name() != Name || !IsWorkflowTool(Name) {
		t.Fatalf("tool name = %q", tool.Name())
	}
	if IsWorkflowTool("task") {
		t.Fatal("the workflow tool claimed the task tool's name")
	}
	schema := tool.Schema()
	if schema["type"] != "object" {
		t.Fatalf("schema type = %#v", schema["type"])
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok || properties["script"] == nil || properties["meta"] == nil {
		t.Fatalf("schema properties = %#v", schema["properties"])
	}
	required, ok := schema["required"].([]string)
	if !ok || len(required) != 2 {
		t.Fatalf("schema required = %#v", schema["required"])
	}
	assertRequired(t, required, "script")
	assertRequired(t, required, "meta")
	if description := tool.Description(); description == "" {
		t.Fatal("tool description is empty")
	}

	result, err := tool.Call(nil, json.RawMessage(`{}`), enginetool.Context{})
	if err == nil || result.ExitCode != 1 {
		t.Fatalf("Call = %#v, %v", result, err)
	}
}

func assertRequired(t *testing.T, required []string, want string) {
	t.Helper()
	for _, name := range required {
		if name == want {
			return
		}
	}
	t.Fatalf("%q is not required by %#v", want, required)
}
