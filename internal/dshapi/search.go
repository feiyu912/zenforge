package dshapi

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/feiyu912/zenforge/internal/dshwire"
)

// The sidebar's search asks the host for sessions whose *message content* matches
// a literal phrase (api/session-controller/src/client/sessions/manager.ts: `Search
// visible session message content`, called from
// client/ui-workspace's `searchSessions`). The reference answers it from a mounted
// query provider, `@deepseek-ai/dsh-session-query`, and this host mounts none: it
// reads the conversations it already lists, out of the same projected logs
// session/list and session/page are served from. What is mirrored exactly is the
// part the client depends on -- one item per session, the reference's own result
// cap, the reference's own snippet bound, and the reference's own refusals for a
// query it cannot search -- so the sidebar renders the same shape whichever host
// answers it (ADR 0127).

const (
	// sessionSearchResultLimit is the reference's cap on one search
	// (SESSION_SEARCH_RESULT_LIMIT = 20, api-session-controller/lib/types/client/
	// types.js). One extra hit is collected to answer `hasMore` without a second
	// pass.
	sessionSearchResultLimit = 20
	// sessionSearchSnippetCodePoints is the reference's snippet bound
	// (SESSION_SEARCH_SNIPPET_MAX_CODE_POINTS = 240). The reference applies it to
	// the provider's excerpt with a longest-prefix truncation, and so does this
	// host, so the served snippet is never longer than the client's own limit.
	sessionSearchSnippetCodePoints = 240
	// sessionSearchQueryMaxUTF16 is the reference's query bound
	// (SESSION_SEARCH_QUERY_MAX_CHARS = 500), counted in the UTF-16 code units
	// JavaScript's `String.length` counts.
	sessionSearchQueryMaxUTF16 = 500
	// sessionSearchLeadCodePoints is how much text before the match the excerpt
	// keeps when the message is longer than the bound: enough for the sentence the
	// phrase sits in, leaving most of the excerpt for what follows the match.
	sessionSearchLeadCodePoints = 60
	// sessionSearchWhitespace is the whitespace class the reference's compiled
	// filter splits and rejoins on. It is deliberately wider than Go's `\s`
	// (ASCII): the reference compiles JavaScript's `\s`, which also covers the
	// Unicode separator categories and the byte-order mark, so a query written with
	// a line separator matches text written with a newline.
	sessionSearchWhitespace = `[\s\p{Z}\x{FEFF}]+`
)

// sessionSearch answers POST /api/session/search.
//
// The conversation is the unit, not the message: a session with a hundred
// matching turns is one row in the sidebar's list, so the newest matching message
// is the one the row shows. The reference derives that one row from a ranked
// index (`bestMatch`, "ranked by its strongest matching event"); this host has no
// ranking and takes the newest match, and the sessions themselves keep the
// session list's order, newest first. Both are recorded in ADR 0127.
func (h *Handler) sessionSearch(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	if failure := rejectUnknownArguments(args, "query"); failure != nil {
		return nil, failure
	}
	query, present, failure := stringArg(args, "query")
	if failure != nil {
		return nil, failure
	}
	if !present {
		// The reference's schema declares `query` as a required string, so a call
		// without one is refused before the handler there and here.
		return nil, fail(codeArgumentsInvalid, `argument "query" is required`,
			map[string]any{"argument": "query"})
	}
	pattern, failure := sessionSearchPattern(query)
	if failure != nil {
		return nil, failure
	}
	sessions, err := h.visibleSessions(ctx)
	if err != nil {
		return nil, fail(codeInternal, "session search failed: "+err.Error(), nil)
	}
	items := make([]map[string]any, 0, sessionSearchResultLimit+1)
	for _, session := range sessions {
		if len(items) > sessionSearchResultLimit {
			break
		}
		if session.Log == nil {
			// A conversation whose log could not be read is still listed (its row
			// is the registry's), but it cannot be searched: skipping it is the
			// honest answer where inventing a match or failing the whole search is
			// not.
			continue
		}
		text, found := sessionSearchMatch(session.Log.Records, pattern)
		if !found {
			continue
		}
		items = append(items, map[string]any{
			"sessionId": session.ID,
			"snippet":   text,
		})
	}
	hasMore := len(items) > sessionSearchResultLimit
	if hasMore {
		items = items[:sessionSearchResultLimit]
	}
	return map[string]any{"items": items, "hasMore": hasMore}, nil
}

// sessionSearchPattern compiles one query the way the reference compiles a literal
// text filter (dsh-session-query's `compileSessionTextFilter`): the phrase is
// data, split on whitespace, its metacharacters escaped, and its parts rejoined so
// any whitespace may separate them -- case-insensitively.
func sessionSearchPattern(query string) (*regexp.Regexp, *methodError) {
	normalized := strings.TrimSpace(query)
	if normalized == "" {
		return nil, fail(codeBadRequest, "session search query must not be empty", nil)
	}
	if units := len(utf16.Encode([]rune(normalized))); units > sessionSearchQueryMaxUTF16 {
		return nil, fail(codeBadRequest,
			fmt.Sprintf("session search query must contain at most %d UTF-16 code units", sessionSearchQueryMaxUTF16),
			nil)
	}
	if strings.ContainsRune(normalized, 0) {
		return nil, fail(codeBadRequest, "session search query must not contain NUL", nil)
	}
	parts := strings.Fields(normalized)
	for index, part := range parts {
		parts[index] = regexp.QuoteMeta(part)
	}
	pattern, err := regexp.Compile("(?i)" + strings.Join(parts, sessionSearchWhitespace))
	if err != nil {
		return nil, fail(codeBadRequest, "session search query is not a searchable phrase: "+err.Error(), nil)
	}
	return pattern, nil
}

// sessionSearchMatch returns the excerpt for one conversation: the newest message
// record whose text matches, bounded to the reference's snippet length. Records
// arrive in ascending served sequence, so the last match is the newest.
func sessionSearchMatch(records []dshwire.Event, pattern *regexp.Regexp) (string, bool) {
	snippet := ""
	found := false
	for _, record := range records {
		text := sessionSearchText(record)
		if text == "" {
			continue
		}
		match := pattern.FindStringIndex(text)
		if match == nil {
			continue
		}
		snippet, found = sessionExcerpt(text, match[0]), true
	}
	return snippet, found
}

// sessionSearchText is the text one projected record contributes to a search: the
// text blocks of a user or assistant message, in order. A user message carries its
// content directly; an assistant message nests the wire message under `message`.
// Every other record type is not message content and contributes nothing, which is
// the same restriction the reference applies with its `user/message` and
// `assistant/message` event filter.
func sessionSearchText(record dshwire.Event) string {
	switch record.Type {
	case "user/message", "assistant/message":
	default:
		return ""
	}
	payload := record.Data
	if nested, ok := payload["message"].(map[string]any); ok {
		payload = nested
	}
	blocks, _ := payload["content"].([]any)
	var builder strings.Builder
	for _, block := range blocks {
		entry, ok := block.(map[string]any)
		if !ok || entry["type"] != "text" {
			continue
		}
		text, _ := entry["text"].(string)
		if text == "" {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString(text)
	}
	return builder.String()
}

// sessionExcerpt is the text a matching row shows: the message itself when it fits
// the bound, and otherwise a window that starts up to
// sessionSearchLeadCodePoints before the match. A window that does not start at
// the beginning is marked with a leading ellipsis, and the whole excerpt is then
// cut to the reference's snippet bound the way the reference cuts it -- a longest
// prefix, so the served snippet can never exceed the client's own limit.
func sessionExcerpt(text string, matchStart int) string {
	runes := []rune(text)
	if len(runes) <= sessionSearchSnippetCodePoints {
		return text
	}
	// matchStart is a byte offset: turn it into the rune offset the window works
	// in, so a message whose match follows multi-byte text is not cut short.
	at := utf8.RuneCountInString(text[:matchStart])
	start := at - sessionSearchLeadCodePoints
	if start < 0 {
		start = 0
	}
	end := start + sessionSearchSnippetCodePoints
	if end > len(runes) {
		end = len(runes)
	}
	excerpt := string(runes[start:end])
	if start > 0 {
		excerpt = "…" + excerpt
	}
	return sessionSearchTruncate(excerpt)
}

// sessionSearchTruncate returns the longest prefix of value containing at most
// sessionSearchSnippetCodePoints Unicode code points -- the reference's own
// `truncateUnicodeCodePoints`, applied where it applies it.
func sessionSearchTruncate(value string) string {
	count, end := 0, 0
	for _, codePoint := range value {
		if count == sessionSearchSnippetCodePoints {
			return value[:end]
		}
		count++
		end += utf8.RuneLen(codePoint)
	}
	return value
}
