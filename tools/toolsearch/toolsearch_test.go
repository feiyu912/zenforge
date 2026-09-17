package toolsearch

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/tool"
)

type fakeTool struct {
	name        string
	description string
	deferred    bool
}

func (t fakeTool) Name() string           { return t.name }
func (t fakeTool) Description() string    { return t.description }
func (t fakeTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (t fakeTool) Call(context.Context, json.RawMessage, tool.Context) (tool.Result, error) {
	return tool.Result{Output: "ok"}, nil
}
func (t fakeTool) DeferredLoading() bool { return t.deferred }

func searchSetup(t *testing.T) tool.Tool {
	t.Helper()
	registry, err := tool.NewRegistry(
		fakeTool{name: "workspace_read", description: "Read files eagerly"},
		fakeTool{name: "mcp__issues__create", description: "Create a tracker issue", deferred: true},
		fakeTool{name: "mcp__issues__list", description: "List tracker issues", deferred: true},
		fakeTool{name: "mcp__billing__invoice", description: "Fetch an invoice", deferred: true},
	)
	if err != nil {
		t.Fatalf("NewRegistry returned error: %v", err)
	}
	search, err := New(Config{Source: registry})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	return search
}

func callSearch(t *testing.T, search tool.Tool, args string) tool.Result {
	t.Helper()
	result, _ := search.Call(context.Background(), json.RawMessage(args), tool.Context{})
	return result
}

func TestToolSearchFindsOnlyDeferredTools(t *testing.T) {
	search := searchSetup(t)
	result := callSearch(t, search, `{"query":"issue"}`)
	if result.Error != "" {
		t.Fatalf("search failed: %+v", result)
	}
	matches, _ := result.Structured["matches"].([]any)
	if len(matches) != 2 {
		t.Fatalf("matches = %#v, want the two deferred issue tools", result.Structured["matches"])
	}
	for _, match := range matches {
		name := match.(map[string]any)["name"]
		if name != "mcp__issues__create" && name != "mcp__issues__list" {
			t.Fatalf("unexpected match %v", name)
		}
	}
	if !strings.Contains(result.Structured["message"].(string), "all are now available") {
		t.Fatalf("message = %v", result.Structured["message"])
	}

	// The eager tool is never returned even when its text matches.
	eager := callSearch(t, search, `{"query":"eagerly"}`)
	if eager.Structured["total"].(float64) != 0 {
		t.Fatalf("eager tool became searchable: %#v", eager.Structured)
	}
}

func TestToolSearchHonorsLimitAndReportsTruncation(t *testing.T) {
	search := searchSetup(t)
	capped := callSearch(t, search, `{"query":"mcp__","limit":2}`)
	if capped.Structured["total"].(float64) != 3 {
		t.Fatalf("total = %v, want 3", capped.Structured["total"])
	}
	if len(capped.Structured["matches"].([]any)) != 2 {
		t.Fatalf("matches = %#v, want 2", capped.Structured["matches"])
	}
	if !strings.Contains(capped.Structured["message"].(string), "returning the first 2") {
		t.Fatalf("message = %v", capped.Structured["message"])
	}

	// An over-large limit clamps to the configured cap instead of erroring.
	over := callSearch(t, search, `{"query":"mcp__","limit":99}`)
	if len(over.Structured["matches"].([]any)) != 3 {
		t.Fatalf("over-limit matches = %#v", over.Structured["matches"])
	}
}

func TestToolSearchReportsNoMatches(t *testing.T) {
	search := searchSetup(t)
	result := callSearch(t, search, `{"query":"kubernetes"}`)
	if result.Structured["total"].(float64) != 0 {
		t.Fatalf("total = %v, want 0", result.Structured["total"])
	}
	if !strings.Contains(result.Structured["message"].(string), "No deferred tools match") {
		t.Fatalf("message = %v", result.Structured["message"])
	}
}

func TestToolSearchRequiresQueryAndSource(t *testing.T) {
	search := searchSetup(t)
	if result := callSearch(t, search, `{"query":"  "}`); !strings.Contains(result.Error, "query is required") {
		t.Fatalf("blank query result = %+v", result)
	}
	if _, err := New(Config{}); err == nil {
		t.Fatal("New accepted a nil source")
	}
}

func TestToolSearchDefaultLimitMatchesCodex(t *testing.T) {
	if DefaultMaxResults != 8 {
		t.Fatalf("DefaultMaxResults = %d, want codex's 8", DefaultMaxResults)
	}
	if Name != "tool_search" {
		t.Fatalf("Name = %q", Name)
	}
}
