package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/tool"
	webtransport "github.com/feiyu912/zenforge/web"
)

type fakeSearcher struct {
	queries []string
	results map[string][]Source
	err     error
}

func (f *fakeSearcher) Search(_ context.Context, query string, limit int) ([]Source, error) {
	f.queries = append(f.queries, query)
	if f.err != nil {
		return nil, f.err
	}
	results := f.results[query]
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func callTool(t *testing.T, instance tool.Tool, raw string) (tool.Result, error) {
	t.Helper()
	return instance.Call(context.Background(), json.RawMessage(raw), tool.Context{RunID: "run_1", ToolCallID: "call_1"})
}

func TestSearchValidatesQueryBoundsAndCollapsesDuplicates(t *testing.T) {
	searcher := &fakeSearcher{results: map[string][]Source{
		"alpha": {{URL: "https://a.example/1"}, {URL: "https://a.example/2"}},
		"beta":  {{URL: "https://a.example/1"}, {URL: "https://b.example/1"}},
	}}
	instance, err := Search(SearchConfig{Searcher: searcher, MaxResults: 3, MaxQueries: 2})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}

	if _, err := callTool(t, instance, `{"queries":[]}`); !errors.Is(err, tool.ErrInvalidArguments) {
		t.Fatalf("empty queries error = %v", err)
	}
	if _, err := callTool(t, instance, `{"queries":["a","b","c"]}`); !errors.Is(err, tool.ErrInvalidArguments) {
		t.Fatalf("over-limit queries error = %v", err)
	}
	if _, err := callTool(t, instance, `{"queries":["  "]}`); !errors.Is(err, tool.ErrInvalidArguments) {
		t.Fatalf("blank query error = %v", err)
	}

	// The bound is checked before collapsing, so three entries exceed a
	// two-query tool even though two survive; a wider tool collapses them.
	deduper, err := Search(SearchConfig{Searcher: searcher, MaxResults: 3, MaxQueries: 4})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	result, err := callTool(t, deduper, `{"queries":["alpha","alpha","beta"]}`)
	if err != nil {
		t.Fatalf("search returned error: %v", err)
	}
	if len(searcher.queries) != 2 {
		t.Fatalf("search queries = %v, want alpha and beta once each", searcher.queries)
	}
	sources, _ := result.Structured["sources"].([]any)
	if len(sources) != 3 {
		t.Fatalf("sources = %#v", result.Structured["sources"])
	}
	// The duplicate URL is collapsed across queries, capped at MaxResults.
	first, _ := sources[0].(map[string]any)
	second, _ := sources[1].(map[string]any)
	third, _ := sources[2].(map[string]any)
	if first["url"] != "https://a.example/1" || second["url"] != "https://a.example/2" || third["url"] != "https://b.example/1" {
		t.Fatalf("sources = %#v", sources)
	}
}

func TestSearchCapsResultsAndReportsEmpty(t *testing.T) {
	searcher := &fakeSearcher{results: map[string][]Source{
		"alpha": {{URL: "https://a.example/1"}, {URL: "https://a.example/2"}},
	}}
	instance, err := Search(SearchConfig{Searcher: searcher, MaxResults: 1})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	result, err := callTool(t, instance, `{"queries":["alpha"]}`)
	if err != nil {
		t.Fatalf("search returned error: %v", err)
	}
	sources, _ := result.Structured["sources"].([]any)
	if len(sources) != 1 {
		t.Fatalf("sources = %#v", sources)
	}

	empty, err := Search(SearchConfig{Searcher: &fakeSearcher{results: map[string][]Source{}}})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	result, err = callTool(t, empty, `{"queries":["nothing"]}`)
	if err != nil {
		t.Fatalf("search returned error: %v", err)
	}
	if message, _ := result.Structured["message"].(string); !strings.Contains(message, "No sources") {
		t.Fatalf("message = %q", message)
	}
}

func TestSearchRequiresApprovalWhenConfigured(t *testing.T) {
	searcher := &fakeSearcher{results: map[string][]Source{"alpha": {{URL: "https://a.example/1"}}}}
	instance, err := Search(SearchConfig{Searcher: searcher, RequireApproval: true})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	result, err := callTool(t, instance, `{"queries":["alpha"]}`)
	if !errors.Is(err, approval.ErrRequired) {
		t.Fatalf("approval error = %v", err)
	}
	if len(searcher.queries) != 0 {
		t.Fatal("search ran before approval")
	}
	request, ok := result.Structured["approval"].(approval.Request)
	if !ok || request.Operation != "web.search" {
		t.Fatalf("approval request = %#v", result.Structured)
	}

	results, err := instance.Call(context.Background(), json.RawMessage(`{"queries":["alpha"]}`), tool.Context{
		RunID:      "run_1",
		ToolCallID: "call_1",
		Metadata: approval.ApprovedMetadata(nil, request,
			approval.Decision{Action: approval.DecisionApprove, Scope: approval.ScopeOnce}),
	})
	if err != nil {
		t.Fatalf("approved search returned error: %v", err)
	}
	if len(searcher.queries) != 1 {
		t.Fatalf("approved search did not run: %v", searcher.queries)
	}
	_ = results
}

func TestFetchFramesUntrustedContentAndTruncates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body><p>" + strings.Repeat("word ", 400) + "</p></body></html>"))
	}))
	defer server.Close()

	instance, err := Fetch(FetchConfig{Policy: webtransport.Policy{AllowPrivate: true}, MaxBodyChars: 600})
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	args, _ := json.Marshal(map[string]string{"url": server.URL})
	result, err := instance.Call(context.Background(), args, tool.Context{RunID: "run_1", ToolCallID: "call_1"})
	if err != nil {
		t.Fatalf("fetch returned error: %v", err)
	}
	message, _ := result.Structured["message"].(string)
	if !strings.Contains(message, "Fetched "+server.URL+" (HTTP 200)") {
		t.Fatalf("message header = %q", message)
	}
	if !strings.Contains(message, ExternalContentNotice) {
		t.Fatalf("message lacks the untrusted-content notice: %q", message)
	}
	if !strings.HasSuffix(message, TruncationFooter) {
		t.Fatalf("message lacks the truncation footer: %q", message)
	}
	if len(message) > 600 {
		t.Fatalf("message length = %d, cap 600", len(message))
	}
	if truncated, _ := result.Structured["truncated"].(bool); !truncated {
		t.Fatalf("truncated flag = %#v", result.Structured)
	}
}

func TestFetchRejectsBlankURLAndBlockedTargets(t *testing.T) {
	instance, err := Fetch(FetchConfig{})
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if _, err := callTool(t, instance, `{"url":"   "}`); !errors.Is(err, tool.ErrInvalidArguments) {
		t.Fatalf("blank url error = %v", err)
	}
	_, err = callTool(t, instance, `{"url":"http://169.254.169.254/latest/meta-data/"}`)
	if !errors.Is(err, tool.ErrToolFailed) || webtransport.ErrorCode(err) != webtransport.CodeBlockedURL {
		t.Fatalf("metadata fetch error = %v (code %q)", err, webtransport.ErrorCode(err))
	}
	if !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("metadata fetch error text = %q", err.Error())
	}
}

func TestHTTPSearcherBuildsRequestAndParsesShapes(t *testing.T) {
	var gotPath, gotQuery, gotHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotHeader = r.Header.Get("X-Subscription-Token")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"web":{"results":[{"title":"T","url":"https://t.example/","description":"D"}]},"results":[{"name":"N","link":"https://n.example/"}]}`))
	}))
	defer server.Close()

	searcher := &HTTPSearcher{Endpoint: server.URL + "/search", APIKey: "secret"}
	sources, err := searcher.Search(context.Background(), "go runtime", 5)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if gotHeader != "secret" || gotPath != "/search" {
		t.Fatalf("request = path %q header %q", gotPath, gotHeader)
	}
	if !strings.Contains(gotQuery, "q=go+runtime") || !strings.Contains(gotQuery, "count=5") {
		t.Fatalf("query = %q", gotQuery)
	}
	if len(sources) != 2 {
		t.Fatalf("sources = %#v", sources)
	}
	if sources[0].Title != "N" || sources[0].URL != "https://n.example/" {
		t.Fatalf("generic shape = %#v", sources[0])
	}
	if sources[1].Title != "T" || sources[1].Snippet != "D" {
		t.Fatalf("brave shape = %#v", sources[1])
	}

	searcher = &HTTPSearcher{Endpoint: server.URL + "/search?engine=x"}
	if _, err := searcher.Search(context.Background(), "q", 3); err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if !strings.Contains(gotQuery, "engine=x") || !strings.Contains(gotQuery, "q=q") {
		t.Fatalf("query with existing params = %q", gotQuery)
	}

	placeholder := &HTTPSearcher{Endpoint: server.URL + "/{query}?limit={limit}"}
	if _, err := placeholder.Search(context.Background(), "a b", 2); err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if gotPath != "/a+b" || !strings.Contains(gotQuery, "limit=2") {
		t.Fatalf("placeholder request = path %q query %q", gotPath, gotQuery)
	}

	if _, err := (&HTTPSearcher{}).Search(context.Background(), "q", 1); !errors.Is(err, tool.ErrInvalidTool) {
		t.Fatalf("missing endpoint error = %v", err)
	}
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer failing.Close()
	if _, err := (&HTTPSearcher{Endpoint: failing.URL}).Search(context.Background(), "q", 1); !errors.Is(err, tool.ErrToolFailed) {
		t.Fatalf("status error = %v", err)
	}
	notJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>nope</html>"))
	}))
	defer notJSON.Close()
	if _, err := (&HTTPSearcher{Endpoint: notJSON.URL}).Search(context.Background(), "q", 1); !errors.Is(err, tool.ErrToolFailed) {
		t.Fatalf("non-JSON error = %v", err)
	}
}
