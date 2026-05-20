# Tool Authoring Guide

This is a draft user-facing guide for writing ZenForge tools.

## Simple Typed Tool

```go
type SearchInput struct {
    Query string `json:"query" jsonschema:"required,description=Search query"`
    Limit int    `json:"limit,omitempty" jsonschema:"description=Maximum result count"`
}

type SearchOutput struct {
    Results []string `json:"results"`
}

search := tools.New("search", "Search internal documents",
    func(ctx context.Context, in SearchInput) (SearchOutput, error) {
        if in.Limit <= 0 {
            in.Limit = 5
        }
        return SearchOutput{Results: []string{"example"}}, nil
    },
)
```

## Manual Tool

Use the low-level interface when a tool needs custom schema or raw JSON input.

```go
type SearchTool struct{}

func (SearchTool) Name() string { return "search" }

func (SearchTool) Description() string {
    return "Search internal documents"
}

func (SearchTool) Schema() map[string]any {
    return map[string]any{
        "type": "object",
        "properties": map[string]any{
            "query": map[string]any{"type": "string"},
        },
        "required": []string{"query"},
    }
}

func (SearchTool) Call(ctx context.Context, input json.RawMessage, call tool.Context) (tool.Result, error) {
    var args struct {
        Query string `json:"query"`
    }
    if err := json.Unmarshal(input, &args); err != nil {
        return tool.Result{Error: "invalid_arguments", ExitCode: -1}, nil
    }
    return tool.Result{
        Output: "found 1 result",
        Structured: map[string]any{
            "results": []string{"example"},
        },
        ExitCode: 0,
    }, nil
}
```

## Result Guidance

Use:

- `Output` for concise model-facing text;
- `Structured` for application data;
- `Error` for machine-readable error codes;
- `ExitCode` for success/failure convention.

Avoid:

- returning secrets;
- dumping huge raw files into `Output`;
- hiding important failure information only in logs.

## Safety

Tools that touch files, shell commands, networks, or external systems should be
wrapped with middleware:

- timeout;
- max output size;
- approval;
- audit logging;
- redaction.

The default built-in shell and workspace tools will use conservative safety
defaults.

