// Package toolsearch provides the codex-style tool_search tool: it
// searches the definitions a run has not loaded yet (tools marked
// deferred through tool.DeferredTool) and returns the matches. The
// agent activates every returned match, so the next model request
// carries those schemas instead of the whole catalog up front.
package toolsearch

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/feiyu912/zenforge/tool"
	"github.com/feiyu912/zenforge/tools"
)

// Name is the tool name exposed to the model.
const Name = "tool_search"

// DefaultMaxResults mirrors codex's search limit.
const DefaultMaxResults = 8

// Description mirrors the reference tool's intent.
const Description = "Search the tool definitions this run has not loaded yet. Deferred tools stay out of the initial tool list; a search returns matching names and descriptions, and every returned match becomes available for the rest of the run."

// Source lists deferred definitions. It is satisfied by
// tool.MemoryRegistry.
type Source interface {
	DefinitionsMatching(include func(tool.Tool) bool) []tool.Definition
}

// Config configures the tool.
type Config struct {
	// Source supplies the deferred definitions. Required.
	Source Source
	// MaxResults caps one search; zero or negative selects
	// DefaultMaxResults.
	MaxResults int
}

type input struct {
	Query string `json:"query" jsonschema:"required,description=Case-insensitive text matched against deferred tool names and descriptions"`
	Limit int    `json:"limit,omitempty" jsonschema:"description=Maximum matches to return; defaults to 8"`
}

// Match is one activated tool definition.
type Match struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type output struct {
	Matches []Match `json:"matches"`
	Total   int     `json:"total"`
	Message string  `json:"message"`
}

// New builds the tool_search tool.
func New(config Config) (tool.Tool, error) {
	if config.Source == nil {
		return nil, fmt.Errorf("%w: deferred tool source is nil", tool.ErrInvalidTool)
	}
	maxResults := config.MaxResults
	if maxResults <= 0 {
		maxResults = DefaultMaxResults
	}
	instance, err := tools.New(Name, Description, func(ctx context.Context, in input) (output, error) {
		query := strings.ToLower(strings.TrimSpace(in.Query))
		if query == "" {
			return output{}, fmt.Errorf("%w: query is required", tool.ErrInvalidArguments)
		}
		limit := in.Limit
		if limit <= 0 || limit > maxResults {
			limit = maxResults
		}
		definitions := config.Source.DefinitionsMatching(tool.IsDeferred)
		matched := make([]Match, 0, len(definitions))
		for _, definition := range definitions {
			haystack := strings.ToLower(definition.Name + " " + definition.Description)
			if !strings.Contains(haystack, query) {
				continue
			}
			matched = append(matched, Match{Name: definition.Name, Description: definition.Description})
		}
		sort.Slice(matched, func(i, j int) bool { return matched[i].Name < matched[j].Name })
		total := len(matched)
		if len(matched) > limit {
			matched = matched[:limit]
		}
		message := searchMessage(query, total, len(matched), limit)
		return output{Matches: matched, Total: total, Message: message}, nil
	})
	if err != nil {
		return nil, err
	}
	// Searching the catalog mutates nothing but the run's activation set,
	// so plan mode allows it.
	return tools.ReadOnly(instance), nil
}

func searchMessage(query string, total, returned, limit int) string {
	if total == 0 {
		return fmt.Sprintf("No deferred tools match %q.", query)
	}
	if total > returned {
		return fmt.Sprintf("Found %d matching tools; returning the first %d. Narrow the query or raise limit (max %d) for the rest.", total, returned, limit)
	}
	return fmt.Sprintf("Found %d matching tools; all are now available.", total)
}
