package mcp

import (
	"context"
	"encoding/json"
)

type Client interface {
	ListTools(ctx context.Context) ([]ToolDefinition, error)
	CallTool(ctx context.Context, name string, arguments json.RawMessage) (CallResult, error)
}
