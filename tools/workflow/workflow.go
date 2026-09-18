// Package workflow exposes the workflow engine as a tool. The tool only
// decodes and describes the request: running the script needs the harness's
// sub-agent runtime, so the loop intercepts the call by name (like the task
// tool) and the Call method here exists to fail clearly if it is ever
// invoked without that runtime.
package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	enginetool "github.com/feiyu912/zenforge/tool"
	engine "github.com/feiyu912/zenforge/workflow"
)

// Name is the tool's model-visible name.
const Name = "workflow"

// New builds the workflow tool.
func New() (enginetool.Tool, error) { return workflowTool{}, nil }

// Must builds the workflow tool and panics if construction fails.
func Must() enginetool.Tool {
	tool, err := New()
	if err != nil {
		panic(err)
	}
	return tool
}

type workflowTool struct{}

func (workflowTool) Name() string { return Name }

func (workflowTool) Description() string {
	return strings.Join([]string{
		"Run a JavaScript orchestration script that fans work out across sub-agents.",
		"The script body runs inside an async function, so top-level `await` works and `return <value>` is its result.",
		"Hooks: `await agent(prompt, opts?)` runs one child agent and resolves to its final text, or to the object behind `opts.schema` when you pass one, and to null when the child did not complete;",
		"`await parallel([() => ..., ...])` runs zero-argument thunks concurrently;",
		"`await pipeline(items, ...stages)` walks each item through the stages independently, calling `stage(previous, item, index)` with no barrier between stages;",
		"`phase(title)` names the phase later agent() calls belong to and `log(message)` narrates progress; `args` is the JSON you passed.",
		"An ordinary error thrown inside a thunk or stage becomes a null item; a misused hook, an unsupported option, a tripped cap, or cancellation fails the whole run.",
		"`opts.schema` must be an object-rooted JSON Schema using only type/oneOf/properties/required/additionalProperties/items/enum/const plus description/title/default/examples.",
		"Return plain JSON data.",
	}, " ")
}

func (workflowTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"script": map[string]any{
				"type":        "string",
				"description": "The JavaScript body to run. It sees agent/parallel/pipeline/phase/log and args.",
			},
			"meta": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":        map[string]any{"type": "string", "description": "A short workflow name."},
					"description": map[string]any{"type": "string", "description": "What the workflow does."},
					"whenToUse":   map[string]any{"type": "string", "description": "Guidance for when this workflow is the right tool."},
					"phases": map[string]any{
						"type":        "array",
						"description": "The declared phases, in order.",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"title":    map[string]any{"type": "string"},
								"detail":   map[string]any{"type": "string"},
								"provider": map[string]any{"type": "string"},
								"model":    map[string]any{"type": "string"},
							},
							"required":             []string{"title"},
							"additionalProperties": false,
						},
					},
				},
				"required":             []string{"name", "description"},
				"additionalProperties": false,
			},
			"args": map[string]any{
				"type":                 "object",
				"description":          "JSON exposed to the script as `args`.",
				"additionalProperties": true,
			},
		},
		"required":             []string{"script", "meta"},
		"additionalProperties": false,
	}
}

// Call fails: the loop runs workflow scripts through its own runtime.
func (workflowTool) Call(ctx context.Context, input json.RawMessage, call enginetool.Context) (enginetool.Result, error) {
	_ = ctx
	_ = call
	err := errors.New("workflow tool requires harness workflow runtime")
	return enginetool.Result{Error: err.Error(), ExitCode: 1}, err
}

// Request is the decoded tool input: the engine's request plus nothing else.
type Request struct {
	Meta   engine.Meta     `json:"meta"`
	Script string          `json:"script"`
	Args   json.RawMessage `json:"args,omitempty"`
}

// Engine maps the decoded request onto the engine's request.
func (r Request) Engine() engine.Request {
	return engine.Request{Meta: r.Meta, Script: r.Script, Args: r.Args}
}

// Decode parses and validates a workflow tool call.
func Decode(raw json.RawMessage) (Request, error) {
	var request Request
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if len(bytes.TrimSpace(raw)) == 0 {
		decoder = json.NewDecoder(strings.NewReader(`{}`))
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(&request); err != nil {
		return Request{}, fmt.Errorf("%w: %v", enginetool.ErrInvalidArguments, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return Request{}, fmt.Errorf("%w: %v", enginetool.ErrInvalidArguments, err)
	}
	if err := request.Validate(); err != nil {
		return Request{}, err
	}
	return request, nil
}

// Validate enforces the tool's contract before any script runs, so a model
// hears about a malformed call as a usage error rather than as a failed run.
func (r Request) Validate() error {
	if strings.TrimSpace(r.Script) == "" {
		return fmt.Errorf("%w: script is required", enginetool.ErrInvalidArguments)
	}
	if strings.TrimSpace(r.Meta.Name) == "" {
		return fmt.Errorf("%w: meta.name is required", enginetool.ErrInvalidArguments)
	}
	if strings.TrimSpace(r.Meta.Description) == "" {
		return fmt.Errorf("%w: meta.description is required", enginetool.ErrInvalidArguments)
	}
	for index, phase := range r.Meta.Phases {
		if strings.TrimSpace(phase.Title) == "" {
			return fmt.Errorf("%w: meta.phases[%d].title is required", enginetool.ErrInvalidArguments, index)
		}
	}
	if len(r.Args) > 0 {
		if !json.Valid(r.Args) {
			return fmt.Errorf("%w: args must be valid JSON", enginetool.ErrInvalidArguments)
		}
		var object map[string]any
		if err := json.Unmarshal(r.Args, &object); err != nil {
			return fmt.Errorf("%w: args must be a JSON object", enginetool.ErrInvalidArguments)
		}
	}
	return nil
}

// IsWorkflowTool reports whether a tool name is the workflow tool.
func IsWorkflowTool(name string) bool { return name == Name }
