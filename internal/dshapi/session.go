package dshapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/internal/dshsession"
	"github.com/feiyu912/zenforge/server/harnesshttp"
	"github.com/feiyu912/zenforge/sessiontitle"
)

const (
	// defaultPageMessages matches the opening-window size the recon records
	// for session/follow; a page without maxMessages is one typical window.
	defaultPageMessages = 50
	// maxPageMessages bounds one session/page response so a hostile or buggy
	// maxMessages cannot make the host build an unbounded JSON array.
	maxPageMessages = 1000
	// titleAppendAttempts bounds the retry loop that commits a rename. The
	// event stores assign the tail seq under their own lock, so a concurrent
	// append between our read and our write is expected and retryable.
	titleAppendAttempts = 8
)

// sessionList answers POST /api/session/list. The list is the run manager's
// view merged with this handler's pending allocations: a pending session is
// blank by definition, and a run's blank stays false because it has a log.
func (h *Handler) sessionList(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	if _, _, failure := stringArg(args, "cursor"); failure != nil {
		return nil, failure
	}
	// The cursor is opaque and this host never issues one, so there is no
	// second page to resume. Accepting and ignoring it is honest; inventing a
	// cursor protocol the client never sees would not be.
	infos, err := h.manager.List(ctx)
	if err != nil {
		return nil, fail(codeInternal, "list runs: "+err.Error(), nil)
	}
	// A session's later turns are listed as one conversation, reported from its
	// newest turn: the console lists sessions, and one conversation appearing
	// once per turn -- each with its own id -- would read as several sessions
	// that happen to share a title.
	known := make(map[string]struct{}, len(infos))
	for _, info := range infos {
		known[info.RunID] = struct{}{}
	}
	newest := make(map[string]harnesshttp.RunInfo, len(infos))
	order := make([]string, 0, len(infos))
	for _, info := range infos {
		sessionID := info.RunID
		// Only a continuation whose base run is present is grouped; an adopted
		// id that merely looks like one keeps its own entry.
		if dshsession.RecognisesBase(info.RunID, known) {
			sessionID, _ = dshsession.Base(info.RunID)
		}
		current, seen := newest[sessionID]
		if !seen {
			order = append(order, sessionID)
			newest[sessionID] = info
			continue
		}
		if info.UpdatedAt.After(current.UpdatedAt) {
			newest[sessionID] = info
		}
	}
	items := make([]map[string]any, 0, len(order)+1)
	for _, sessionID := range order {
		item := h.summaryFor(ctx, newest[sessionID])
		item["sessionId"] = sessionID
		items = append(items, item)
	}
	h.mu.Lock()
	pending := make([]pendingSession, 0, len(h.pending))
	for _, session := range h.pending {
		pending = append(pending, session)
	}
	h.mu.Unlock()
	for _, session := range pending {
		items = append(items, map[string]any{
			"sessionId": session.id,
			"updatedAt": session.createdAt.UnixMilli(),
			"running":   false,
			"blank":     true,
		})
	}
	// Newest first, with the session id as a stable tie-break so concurrent
	// creates in the same millisecond still order deterministically.
	sort.Slice(items, func(i, j int) bool {
		left, right := items[i], items[j]
		leftAt, _ := left["updatedAt"].(int64)
		rightAt, _ := right["updatedAt"].(int64)
		if leftAt != rightAt {
			return leftAt > rightAt
		}
		leftID, _ := left["sessionId"].(string)
		rightID, _ := right["sessionId"].(string)
		return leftID < rightID
	})
	return map[string]any{"items": items}, nil
}

// summaryFor maps one RunInfo to a SessionSummary. origin and parentSessionId
// are omitted: the run manager has no subagent lineage to report, and an
// omitted optional field is honest where a fabricated one is not.
func (h *Handler) summaryFor(ctx context.Context, info harnesshttp.RunInfo) map[string]any {
	item := map[string]any{
		"sessionId": info.RunID,
		"updatedAt": info.UpdatedAt.UnixMilli(),
		"running":   runActive(info.Status),
		"blank":     false,
	}
	if title, ok := h.latestTitle(ctx, info.RunID); ok {
		item["title"] = title
	}
	return item
}

// latestTitle reads the newest session.title event from a run's durable log.
// Rename is the writer, and the agent itself emits the same event at run
// start, so the log is the single source of a session's title.
func (h *Handler) latestTitle(ctx context.Context, runID string) (string, bool) {
	events, err := h.events.Read(ctx, runID, 0, 0)
	if err != nil {
		return "", false
	}
	title := ""
	for _, event := range events {
		if event.Type != zenforge.EventSessionTitle {
			continue
		}
		if value, ok := event.Payload["title"].(string); ok && value != "" {
			title = value
		}
	}
	return title, title != ""
}

// sessionCreate answers POST /api/session/create. It either adopts an explicit
// session id or allocates one, and remembers an unstarted session so the first
// prompt can start a run under that exact id.
func (h *Handler) sessionCreate(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	workspaceID, _, failure := stringArg(args, "workspaceId")
	if failure != nil {
		return nil, failure
	}
	cwd, _, failure := stringArg(args, "cwd")
	if failure != nil {
		return nil, failure
	}
	preset, _, failure := stringArg(args, "agentPreset")
	if failure != nil {
		return nil, failure
	}
	requestedID, _, failure := stringArg(args, "sessionId")
	if failure != nil {
		return nil, failure
	}
	// These fields describe a per-session execution context the run manager
	// does not have: runs share the agent's configured working directory and
	// preset. Rejecting them beats silently creating a session that ignores
	// what the caller asked for.
	if strings.TrimSpace(workspaceID) != "" {
		return nil, fail(codeUnimplemented,
			"session/create workspaceId is not supported: a session is one run and this host has no workspace grouping to attach it to",
			map[string]any{"field": "workspaceId"})
	}
	if strings.TrimSpace(cwd) != "" {
		return nil, fail(codeUnimplemented,
			"session/create cwd is not supported: the run manager cannot override the agent's configured working directory per run",
			map[string]any{"field": "cwd"})
	}
	if strings.TrimSpace(preset) != "" {
		return nil, fail(codeUnimplemented,
			"session/create agentPreset is not supported: the run manager cannot select a per-run agent preset",
			map[string]any{"field": "agentPreset"})
	}

	sessionID := strings.TrimSpace(requestedID)
	if sessionID == "" {
		sessionID = zenforge.NewRunID()
		if failure := h.rememberPending(sessionID); failure != nil {
			return nil, failure
		}
		return map[string]any{"sessionId": sessionID}, nil
	}
	if err := validateSessionID(sessionID); err != nil {
		return nil, fail(codeArgumentsInvalid, err.Error(), map[string]any{"argument": "sessionId"})
	}
	// An explicit id means "adopt this session". A live run or an existing
	// durable log is already a session; only a truly unknown id becomes a
	// pending allocation, so adopting twice never resets state.
	if _, err := h.manager.Get(sessionID); err == nil {
		return map[string]any{"sessionId": sessionID}, nil
	} else if !errors.Is(err, harnesshttp.ErrRunNotFound) {
		return nil, fail(codeInternal, "look up session: "+err.Error(), nil)
	}
	latest, err := h.events.LatestSeq(ctx, sessionID)
	if err != nil {
		return nil, fail(codeInternal, "read session log: "+err.Error(), nil)
	}
	if latest > 0 {
		return map[string]any{"sessionId": sessionID}, nil
	}
	if failure := h.rememberPending(sessionID); failure != nil {
		return nil, failure
	}
	return map[string]any{"sessionId": sessionID}, nil
}

// sessionPrompt answers POST /api/session/prompt. A pending session starts a
// run with its pre-assigned id; an active run receives the text as a queued
// turn through RunManager.Steer. Both modes the console offers map to that
// single queue path: the repository models a turn boundary, not immediate
// interruption, and claiming otherwise would be a lie.
func (h *Handler) sessionPrompt(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	requestID, _, failure := stringArg(args, "requestId")
	if failure != nil {
		return nil, failure
	}
	sessionID, _, failure := stringArg(args, "sessionId")
	if failure != nil {
		return nil, failure
	}
	mode, _, failure := stringArg(args, "mode")
	if failure != nil {
		return nil, failure
	}
	if _, _, failure := stringArg(args, "clientTimeZone"); failure != nil {
		return nil, failure
	}
	if strings.TrimSpace(requestID) == "" {
		return nil, argumentRequired("requestId")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, argumentRequired("sessionId")
	}
	if mode != "queue" && mode != "steer" {
		return nil, fail(codeArgumentsInvalid, `"mode" must be "queue" or "steer"`,
			map[string]any{"argument": "mode", "value": mode})
	}
	rawContent, ok := args["content"]
	if !ok {
		return nil, argumentRequired("content")
	}
	text, failure := decodePromptContent(rawContent)
	if failure != nil {
		return nil, failure
	}

	// A prompt may name a continuation run id rather than the session's first
	// turn, and either names the same conversation.
	sessionID = h.resolveSession(ctx, sessionID)

	if h.takePending(sessionID) {
		if _, err := h.manager.Start(ctx, zenforge.Task{RunID: sessionID, Input: text}); err != nil {
			// The allocation survives a start that never happened, so the
			// console can retry the prompt without re-creating the session.
			_ = h.rememberPending(sessionID)
			return nil, startFailure(sessionID, err)
		}
		return map[string]any{"accepted": true}, nil
	}

	runIDs := h.sessionRunIDs(ctx, sessionID)
	if len(runIDs) == 0 {
		// No turn exists: the id is unknown, which is a different answer from a
		// conversation this host cannot continue.
		return nil, fail(codeSessionNotFound, fmt.Sprintf("session %q not found", sessionID),
			map[string]any{"sessionId": sessionID})
	}
	current := runIDs[len(runIDs)-1]
	info, err := h.manager.Get(current)
	switch {
	case err == nil && runActive(info.Status):
		if _, err := h.manager.Steer(current, strings.TrimSpace(requestID), text); err != nil {
			// Get found the run, so Steer's not-found here means this manager
			// cannot reach it: a shared registry lists other processes' runs but
			// does not deliver turns to them.
			if errors.Is(err, harnesshttp.ErrRunNotFound) {
				return nil, fail(codeUnimplemented,
					fmt.Sprintf("session %q is active but not owned by this process; a queued turn cannot be delivered to it", sessionID),
					map[string]any{"sessionId": sessionID, "mode": mode})
			}
			return nil, steerFailure(sessionID, mode, err)
		}
		return map[string]any{"accepted": true}, nil
	case err != nil && !errors.Is(err, harnesshttp.ErrRunNotFound):
		return nil, fail(codeInternal, "look up session: "+err.Error(), nil)
	}

	// The session's newest turn is finished (or its run is no longer tracked by
	// this process), so the prompt starts the next turn of the same
	// conversation: a new run id from the chain, carrying the exchange so far.
	turn := dshsession.NextTurn(runIDs)
	continuationID := dshsession.ContinuationRunID(sessionID, turn)
	task := zenforge.Task{
		RunID:           continuationID,
		Input:           text,
		InitialMessages: h.conversationMessages(ctx, runIDs),
	}
	if _, err := h.manager.Start(ctx, task); err != nil {
		return nil, startFailure(continuationID, err)
	}
	return map[string]any{"accepted": true}, nil
}

// decodePromptContent flattens the console content parts into the single text
// a run takes. Image and file parts are refused by name: the run manager has
// no attachment intake, and accepting them while dropping the bytes would be
// a lie the user only discovers later.
func decodePromptContent(raw json.RawMessage) (string, *methodError) {
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", fail(codeArgumentsInvalid, `"content" must be an array of content parts`,
			map[string]any{"argument": "content"})
	}
	if len(parts) == 0 {
		return "", fail(codeArgumentsInvalid, `"content" must contain at least one part`,
			map[string]any{"argument": "content"})
	}
	texts := make([]string, 0, len(parts))
	for _, rawPart := range parts {
		part, err := decodeJSONObject(rawPart)
		if err != nil {
			return "", fail(codeArgumentsInvalid, `each "content" part must be a JSON object`,
				map[string]any{"argument": "content"})
		}
		rawType, ok := part["type"]
		if !ok {
			return "", fail(codeArgumentsInvalid, `each "content" part requires a "type"`,
				map[string]any{"argument": "content"})
		}
		var partType string
		if err := json.Unmarshal(rawType, &partType); err != nil {
			return "", fail(codeArgumentsInvalid, `content part "type" must be a string`,
				map[string]any{"argument": "content"})
		}
		switch partType {
		case "text":
			rawText, ok := part["text"]
			if !ok {
				return "", fail(codeArgumentsInvalid, `a "text" content part requires "text"`,
					map[string]any{"argument": "content"})
			}
			var value string
			if err := json.Unmarshal(rawText, &value); err != nil {
				return "", fail(codeArgumentsInvalid, `content part "text" must be a string`,
					map[string]any{"argument": "content"})
			}
			if value = strings.TrimSpace(value); value != "" {
				texts = append(texts, value)
			}
		default:
			return "", fail(codeUnsupportedContent,
				fmt.Sprintf("prompt content part %q is not supported: this host accepts text parts only", partType),
				map[string]any{"part": partType})
		}
	}
	if len(texts) == 0 {
		return "", fail(codeArgumentsInvalid, `"content" must contain at least one non-whitespace text part`,
			map[string]any{"argument": "content"})
	}
	return strings.Join(texts, "\n\n"), nil
}

// sessionCancel answers POST /api/session/cancel. Cancel is idempotent for an
// already-cancelled run; any other terminal state is reported as a conflict
// rather than dressed up as a cancellation that did not happen.
func (h *Handler) sessionCancel(_ context.Context, args map[string]json.RawMessage) (any, *methodError) {
	sessionID, _, failure := stringArg(args, "sessionId")
	if failure != nil {
		return nil, failure
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, argumentRequired("sessionId")
	}
	if err := h.manager.Cancel(sessionID); err != nil {
		switch {
		case errors.Is(err, harnesshttp.ErrRunNotFound):
			return nil, fail(codeSessionNotFound, fmt.Sprintf("session %q not found", sessionID),
				map[string]any{"sessionId": sessionID})
		case errors.Is(err, harnesshttp.ErrRunTerminal):
			status := "terminal"
			if info, getErr := h.manager.Get(sessionID); getErr == nil {
				status = string(info.Status)
			}
			return nil, fail(codeSessionConflict,
				fmt.Sprintf("session %q already finished with status %q; cancel is a no-op", sessionID, status),
				map[string]any{"sessionId": sessionID, "status": status})
		case errors.Is(err, harnesshttp.ErrInvalidRunID):
			return nil, argumentRequired("sessionId")
		default:
			return nil, fail(codeInternal, "cancel run: "+err.Error(), nil)
		}
	}
	return map[string]any{"accepted": true}, nil
}

// sessionRename answers POST /api/session/rename by committing one durable
// session.title event, which is where the repository already keeps a title.
// The returned seq is the event's durable position, read from the log rather
// than predicted.
func (h *Handler) sessionRename(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	sessionID, _, failure := stringArg(args, "sessionId")
	if failure != nil {
		return nil, failure
	}
	title, _, failure := stringArg(args, "title")
	if failure != nil {
		return nil, failure
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, argumentRequired("sessionId")
	}
	normalized := sessiontitle.Normalize(title, sessiontitle.DefaultMaxTitleBytes)
	if normalized == "" {
		return nil, fail(codeTitleInvalid, `"title" must not be empty after normalization`,
			map[string]any{"argument": "title"})
	}
	latest, err := h.events.LatestSeq(ctx, sessionID)
	if err != nil {
		return nil, fail(codeInternal, "read session log: "+err.Error(), nil)
	}
	if latest == 0 {
		if h.isPending(sessionID) {
			return nil, fail(codeUnimplemented,
				"session/rename is unsupported before a session's run has started: a pending session has no durable log to commit a title to",
				map[string]any{"sessionId": sessionID})
		}
		return nil, fail(codeSessionNotFound, fmt.Sprintf("session %q not found", sessionID),
			map[string]any{"sessionId": sessionID})
	}
	seq, err := h.appendTitle(ctx, sessionID, normalized)
	if err != nil {
		return nil, fail(codeInternal, "commit title: "+err.Error(), nil)
	}
	return map[string]any{"title": normalized, "seq": seq}, nil
}

// appendTitle commits one session.title event and returns its seq. The seq is
// computed against the current tail and the append is retried if a concurrent
// writer consumed it first, so the returned position identifies this event.
func (h *Handler) appendTitle(ctx context.Context, runID, title string) (int64, error) {
	var lastErr error
	for attempt := 0; attempt < titleAppendAttempts; attempt++ {
		latest, err := h.events.LatestSeq(ctx, runID)
		if err != nil {
			return 0, err
		}
		event := zenforge.NewEvent(zenforge.EventSessionTitle, runID, map[string]any{
			"title": title, "source": "user",
		})
		event.Seq = zenforge.NextEventSeq(latest)
		if err := h.events.Append(ctx, event); err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return 0, lastErr
			}
			continue
		}
		return event.Seq, nil
	}
	return 0, fmt.Errorf("event log tail did not settle after %d attempts: %w", titleAppendAttempts, lastErr)
}

// currentRun resolves a session to the run serving its newest turn, stopping at
// the first turn that does not exist. It is the same walk internal/dshstream
// performs for follow, kept here so the RPC surface and the stream agree on
// which run a session currently is.
func (h *Handler) currentRun(ctx context.Context, sessionID string) string {
	runID := sessionID
	for turn := 2; ; turn++ {
		next := dshsession.ContinuationRunID(sessionID, turn)
		if !h.runExists(ctx, next) {
			return runID
		}
		runID = next
	}
}

// sessionPage answers POST /api/session/page with a backwards page of the
// durable log. throughSeq is the inclusive cursor; beforeSeq, when present, is
// the exclusive upper bound of the returned page, which is how the console
// asks for the window before the records it already loaded.
func (h *Handler) sessionPage(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	sessionID, failure := decodePageAddress(args)
	if failure != nil {
		return nil, failure
	}
	throughSeq, present, failure := intArg(args, "throughSeq")
	if failure != nil {
		return nil, failure
	}
	if !present {
		return nil, argumentRequired("throughSeq")
	}
	if throughSeq < -1 {
		return nil, fail(codeArgumentsInvalid, `"throughSeq" must be an integer greater than or equal to -1`,
			map[string]any{"argument": "throughSeq"})
	}
	beforeSeq, hasBefore, failure := intArg(args, "beforeSeq")
	if failure != nil {
		return nil, failure
	}
	if hasBefore && beforeSeq < 0 {
		return nil, fail(codeArgumentsInvalid, `"beforeSeq" must be a non-negative integer`,
			map[string]any{"argument": "beforeSeq"})
	}
	maxMessages, hasMax, failure := intArg(args, "maxMessages")
	if failure != nil {
		return nil, failure
	}
	if hasMax && maxMessages <= 0 {
		return nil, fail(codeArgumentsInvalid, `"maxMessages" must be a positive integer`,
			map[string]any{"argument": "maxMessages"})
	}
	if !hasMax {
		maxMessages = defaultPageMessages
	}
	if maxMessages > maxPageMessages {
		maxMessages = maxPageMessages
	}

	// Read the run serving the session's newest turn, which is the run follow
	// streams: a page read from the first turn while follow streamed the second
	// would show one conversation's history beside another's answer.
	//
	// This host does not yet merge a session's turns into one paged log. The
	// wire cursor is a sequence number, and each run's log numbers its own
	// events from one, so a merged page would need a synthetic coordinate and
	// rewritten per-event ids. Until that exists, the page is the newest turn's
	// log and an earlier turn's transcript is not reachable through it; ADR 0086
	// records that as a known gap rather than hiding it.
	runID := h.currentRun(ctx, sessionID)
	events, err := h.events.Read(ctx, runID, 0, 0)
	if err != nil {
		return nil, fail(codeInternal, "read session log: "+err.Error(), nil)
	}
	if len(events) == 0 {
		return nil, fail(codeSessionNotFound, fmt.Sprintf("session %q not found", sessionID),
			map[string]any{"sessionId": sessionID})
	}
	latest := events[len(events)-1].Seq
	cursor := throughSeq
	if cursor < 0 || cursor > latest {
		cursor = latest
	}
	upper := cursor + 1
	if hasBefore && beforeSeq < upper {
		upper = beforeSeq
	}
	selected := make([]zenforge.Event, 0, maxMessages)
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Seq >= upper {
			continue
		}
		if int64(len(selected)) >= maxMessages {
			break
		}
		selected = append(selected, event)
	}
	records := make([]map[string]any, 0, len(selected))
	for index := len(selected) - 1; index >= 0; index-- {
		records = append(records, map[string]any{"type": "event", "event": wireEvent(selected[index])})
	}
	hasMore := false
	if len(selected) > 0 {
		hasMore = selected[len(selected)-1].Seq > events[0].Seq
	}
	return map[string]any{"records": records, "hasMore": hasMore}, nil
}

// decodePageAddress validates a SessionAddress and returns its session id.
// Only the top-level "session" arm is supported; a subagent address names a
// child session this host does not model.
func decodePageAddress(args map[string]json.RawMessage) (string, *methodError) {
	rawAddress, ok := args["address"]
	if !ok {
		return "", argumentRequired("address")
	}
	address, err := decodeJSONObject(rawAddress)
	if err != nil {
		return "", fail(codeArgumentsInvalid, `"address" must be a JSON object`, map[string]any{"argument": "address"})
	}
	kind := ""
	if rawKind, ok := address["kind"]; ok {
		if err := json.Unmarshal(rawKind, &kind); err != nil {
			return "", fail(codeArgumentsInvalid, `"address.kind" must be a string`, map[string]any{"argument": "address"})
		}
	}
	switch kind {
	case "session", "":
		// An address without a kind is tolerated; the only arm this host
		// serves is the top-level session.
	case "subagent":
		return "", fail(codeUnimplemented,
			"session/page does not support subagent addresses: this host serves top-level runs only",
			map[string]any{"addressKind": "subagent"})
	default:
		return "", fail(codeArgumentsInvalid, fmt.Sprintf("address.kind %q is unknown", kind),
			map[string]any{"argument": "address"})
	}
	sessionID := ""
	if rawID, ok := address["sessionId"]; ok {
		if err := json.Unmarshal(rawID, &sessionID); err != nil {
			return "", fail(codeArgumentsInvalid, `"address.sessionId" must be a string`,
				map[string]any{"argument": "address"})
		}
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", argumentRequired("address.sessionId")
	}
	return sessionID, nil
}

// wireEvent maps one durable zenforge event to the console's SessionWireEvent
// envelope. Every event is an append on the surface, the payload stays as the
// event's own JSON, and unknown event names are legal — the client renders
// them opaquely rather than failing.
func wireEvent(event zenforge.Event) map[string]any {
	data := map[string]any(event.Payload)
	if data == nil {
		data = map[string]any{}
	}
	return map[string]any{
		"type":      string(event.Type),
		"seq":       event.Seq,
		"time":      event.Timestamp,
		"data":      data,
		"surfaceOp": "append",
	}
}

// runActive reports whether a run status is a live run for the console's
// running flag: starting, running, and waiting on approval are all live.
func runActive(status harnesshttp.RunStatus) bool {
	switch status {
	case harnesshttp.RunStarting, harnesshttp.RunRunning, harnesshttp.RunWaitingApproval:
		return true
	default:
		return false
	}
}

func finishedRunMessage(sessionID, status string) string {
	return fmt.Sprintf("session %q (%s) cannot accept a new prompt: this host maps one console session to one zenforge run, and a run accepts either a fresh start or a queued turn while it is active", sessionID, status)
}

func startFailure(sessionID string, err error) *methodError {
	switch {
	case errors.Is(err, harnesshttp.ErrRunExists), errors.Is(err, harnesshttp.ErrEventsExist):
		return fail(codeSessionConflict, fmt.Sprintf("session %q already has a run or a durable log", sessionID),
			map[string]any{"sessionId": sessionID})
	case errors.Is(err, harnesshttp.ErrManagerClosed), errors.Is(err, harnesshttp.ErrEventsRequired):
		return fail(codeServiceUnavailable, "run manager is unavailable: "+err.Error(), nil)
	default:
		return fail(codeInternal, "start run: "+err.Error(), nil)
	}
}

func steerFailure(sessionID, mode string, err error) *methodError {
	switch {
	case errors.Is(err, harnesshttp.ErrRunTerminal):
		return fail(codeSessionConflict, fmt.Sprintf("session %q already finished", sessionID),
			map[string]any{"sessionId": sessionID})
	case errors.Is(err, harnesshttp.ErrSteerUnavailable):
		return fail(codeUnimplemented,
			"the configured agent does not accept queued user turns: RunManager.Steer reports the run cannot be steered",
			map[string]any{"sessionId": sessionID, "mode": mode})
	default:
		return fail(codeInternal, "steer run: "+err.Error(), nil)
	}
}

func (h *Handler) rememberPending(sessionID string) *methodError {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.pending[sessionID]; exists {
		return nil
	}
	if len(h.pending) >= maxPendingSessions {
		return fail(codeLimitExceeded, "too many pending sessions", nil)
	}
	h.pending[sessionID] = pendingSession{id: sessionID, createdAt: time.Now().UTC()}
	return nil
}

func (h *Handler) takePending(sessionID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.pending[sessionID]; !exists {
		return false
	}
	delete(h.pending, sessionID)
	return true
}

func (h *Handler) isPending(sessionID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, exists := h.pending[sessionID]
	return exists
}

func validateSessionID(sessionID string) error {
	if sessionID == "." || sessionID == ".." || strings.ContainsAny(sessionID, `/\`) {
		return fmt.Errorf("sessionId %q is not a valid run id", sessionID)
	}
	return nil
}

// stringArg reads an optional string argument. A present non-string value is a
// method-level argument error, not a protocol error: the envelope was fine.
func stringArg(args map[string]json.RawMessage, key string) (string, bool, *methodError) {
	raw, ok := args[key]
	if !ok {
		return "", false, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false, fail(codeArgumentsInvalid, fmt.Sprintf("argument %q must be a string", key),
			map[string]any{"argument": key})
	}
	return value, true, nil
}

// intArg reads an optional integer argument. JSON numbers only: a string or a
// fractional value is an argument error.
func intArg(args map[string]json.RawMessage, key string) (int64, bool, *methodError) {
	raw, ok := args[key]
	if !ok {
		return 0, false, nil
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, false, fail(codeArgumentsInvalid, fmt.Sprintf("argument %q must be an integer", key),
			map[string]any{"argument": key})
	}
	return value, true, nil
}
