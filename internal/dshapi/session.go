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
	"github.com/feiyu912/zenforge/internal/dshwire"
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

// visibleSession is one conversation the console's session list shows: the
// grouped id, the newest run behind it, the row this host serves for it, and the
// conversation's projected log when it could be read. session/list renders the
// rows; session/search walks the logs. Sharing the enumeration is what keeps a
// session searchable exactly when it is listed -- the grouping, the blank filter
// and the newest-first order are one rule, not two.
type visibleSession struct {
	ID   string
	Info harnesshttp.RunInfo
	Item map[string]any
	Log  *dshwire.SessionLog
}

// visibleSessions is the console's session list as values: every entry the list
// renders, in the order it renders them.
func (h *Handler) visibleSessions(ctx context.Context) ([]visibleSession, error) {
	infos, err := h.manager.List(ctx)
	if err != nil {
		return nil, err
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
	sessions := make([]visibleSession, 0, len(order)+1)
	for _, sessionID := range order {
		item, log, listable := h.listableSession(ctx, sessionID, newest[sessionID])
		if !listable {
			continue
		}
		item["sessionId"] = sessionID
		sessions = append(sessions, visibleSession{ID: sessionID, Info: newest[sessionID], Item: item, Log: log})
	}
	h.mu.Lock()
	pending := make([]pendingSession, 0, len(h.pending))
	for _, session := range h.pending {
		pending = append(pending, session)
	}
	h.mu.Unlock()
	for _, session := range pending {
		sessions = append(sessions, visibleSession{
			ID: session.id,
			Info: harnesshttp.RunInfo{
				RunID:     session.id,
				UpdatedAt: session.createdAt,
			},
			Item: map[string]any{
				"sessionId": session.id,
				"updatedAt": session.createdAt.UnixMilli(),
				"running":   false,
				"blank":     true,
			},
		})
	}
	// Newest first, with the session id as a stable tie-break so concurrent
	// creates in the same millisecond still order deterministically. The
	// comparison is on the millisecond the row serves rather than the finer time
	// behind it: the client sorts the rows it received, so the served array order
	// and the served `updatedAt` have to agree even for two creates inside one
	// millisecond, which the id then breaks.
	sort.Slice(sessions, func(i, j int) bool {
		left, right := sessions[i], sessions[j]
		leftAt, rightAt := left.Info.UpdatedAt.UnixMilli(), right.Info.UpdatedAt.UnixMilli()
		if leftAt != rightAt {
			return leftAt > rightAt
		}
		return left.ID < right.ID
	})
	return sessions, nil
}

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
	sessions, err := h.visibleSessions(ctx)
	if err != nil {
		return nil, fail(codeInternal, "list runs: "+err.Error(), nil)
	}
	items := make([]map[string]any, 0, len(sessions))
	for _, session := range sessions {
		items = append(items, session.Item)
	}
	return map[string]any{"items": items}, nil
}

// listableSession maps one RunInfo to a SessionSummary, and reports whether the
// console can actually open it. origin is omitted: the run manager has no
// subagent lineage to report, and an omitted optional field is honest where a
// fabricated one is not. parentSessionId is served when the conversation was
// forked -- a fork writes its source into the child's first turn -- and omitted
// otherwise, for the same reason.
//
// A record whose run never wrote an event is a start that failed before the
// transcript began. The console answers not-found for such a session, so
// listing it offers the operator a conversation that cannot be opened --
// and with a durable registry that record would otherwise be listed forever.
// A live run is kept: it is between its claim and its first event only for a
// moment, and the console shows it as running.
func (h *Handler) listableSession(ctx context.Context, sessionID string, info harnesshttp.RunInfo) (map[string]any, *dshwire.SessionLog, bool) {
	events, err := h.events.Read(ctx, info.RunID, 0, 0)
	if err != nil {
		events = nil
	}
	live := info.Live(time.Now())
	if len(events) == 0 && !live {
		return nil, nil, false
	}
	item := map[string]any{
		"sessionId": info.RunID,
		"updatedAt": info.UpdatedAt.UnixMilli(),
		// Liveness is the run's own answer, not the record's status: a registry
		// row left by a process that died keeps an active status and an expired
		// lease, and the console must not show that conversation as running
		// forever (ADR 0109).
		"running": live,
		"blank":   false,
	}
	// The name travels as the `title` projection, with the served sequence that set
	// it as the watermark -- not as a field of its own. The client seeds each row's
	// projection store from this block and reads the row title from the store, so a
	// top-level title is a field nothing renders (ADR 0119). The title is read from
	// the session's whole log, because a rename can have landed on any turn.
	log, err := dshwire.Session(ctx, h, sessionID, func(turn int) dshwire.Identity {
		identity := h.wireIdentity(sessionID)
		identity.Turn = turn
		return identity
	})
	if err != nil {
		log = nil
	}
	if log != nil {
		if title, seq := log.Title(); title != "" {
			item["projections"] = map[string]any{
				"asOfSeq": seq,
				"values":  map[string]any{dshwire.TitleProjection: title},
			}
		}
		// A forked conversation names the one it came from, which is how the
		// console nests it under its source (flattenLineage) instead of showing it
		// as a root row. The field is absent -- not empty -- for a conversation
		// nobody forked.
		if log.ParentSessionID != "" {
			item["parentSessionId"] = log.ParentSessionID
		}
	}
	return item, log, true
}

// sessionCreate answers POST /api/session/create. It either adopts an explicit
// session id or allocates one, and remembers an unstarted session so the first
// prompt can start a run under that exact id.
func (h *Handler) sessionCreate(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	// Every field the client's SessionCreateRequest can carry, and nothing else:
	// a typo used to be ignored, which is how a wrapped workspaceId went
	// unnoticed while the console looked like it was grouping sessions.
	if failure := rejectUnknownArguments(args, "workspaceId", "cwd", "agentPreset", "sessionId"); failure != nil {
		return nil, failure
	}
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
	// The console's workspace flow creates its session with the workspace it
	// just adopted, so the id has to resolve. It resolves to a session only
	// when the row points at the directory this host actually runs in: runs
	// share the agent's configured working directory, and grouping a session
	// under a different directory would tell the console it edits files the
	// tools never touch.
	requestedWorkspace := strings.TrimSpace(workspaceID)
	if requestedWorkspace != "" {
		if _, failure := h.workspaceViewForSession(requestedWorkspace); failure != nil {
			return nil, failure
		}
	}
	// These remaining fields describe a per-session execution context the run
	// manager does not have: runs share the agent's configured working
	// directory and preset. Rejecting them beats silently creating a session
	// that ignores what the caller asked for.
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

	// Every session this host creates runs in its one workspace, so the console
	// groups it there whether or not the caller named it: a session missing
	// from its workspace's session list is a row the sidebar cannot show, even
	// though the session exists.
	answer := func(sessionID string) (any, *methodError) {
		if workspaces := h.workspaceRegistry(); workspaces != nil {
			if err := workspaces.AttachSession(requestedWorkspace, sessionID); err != nil {
				return nil, workspaceFailure(err)
			}
		}
		// A session created after the control stream opened is announced to it, so
		// the console's projection store is seeded with the modelSelection key for
		// this session too. Without it the composer's model control holds
		// "Loading models…" with no groups for every session created during the
		// connection (ADR 0102).
		if store := h.modelSelectionStore(); store != nil {
			if registrar, ok := store.(sessionRegistrar); ok {
				registrar.RegisterSession(sessionID)
			}
		}
		return map[string]any{"sessionId": sessionID}, nil
	}

	sessionID := strings.TrimSpace(requestedID)
	if sessionID == "" {
		sessionID = zenforge.NewRunID()
		if failure := h.rememberPending(sessionID); failure != nil {
			return nil, failure
		}
		return answer(sessionID)
	}
	if err := validateSessionID(sessionID); err != nil {
		return nil, fail(codeArgumentsInvalid, err.Error(), map[string]any{"argument": "sessionId"})
	}
	// An explicit id means "adopt this session". A live run or an existing
	// durable log is already a session; only a truly unknown id becomes a
	// pending allocation, so adopting twice never resets state.
	if _, err := h.manager.Get(sessionID); err == nil {
		return answer(sessionID)
	} else if !errors.Is(err, harnesshttp.ErrRunNotFound) {
		return nil, fail(codeInternal, "look up session: "+err.Error(), nil)
	}
	latest, err := h.events.LatestSeq(ctx, sessionID)
	if err != nil {
		return nil, fail(codeInternal, "read session log: "+err.Error(), nil)
	}
	if latest > 0 {
		return answer(sessionID)
	}
	if failure := h.rememberPending(sessionID); failure != nil {
		return nil, failure
	}
	return answer(sessionID)
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
		if failure := h.applyModelSelection(sessionID); failure != nil {
			// The allocation survives so the console can retry after fixing the
			// selection; starting the run anyway would use a model the operator
			// did not choose.
			_ = h.rememberPending(sessionID)
			return nil, failure
		}
		if _, err := h.manager.Start(ctx, zenforge.Task{
			RunID:    sessionID,
			Input:    text,
			PromptID: strings.TrimSpace(requestID),
		}); err != nil {
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
	case err == nil && info.Live(time.Now()):
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

	// The session's newest turn is finished, its run is no longer tracked by this
	// process, or its lease expired with the process that owned it (ADR 0109).
	// Any of those means the prompt starts the next turn of the same
	// conversation: a new run id from the chain, carrying the exchange so far.
	turn := dshsession.NextTurn(runIDs)
	continuationID := dshsession.ContinuationRunID(sessionID, turn)
	if failure := h.applyModelSelection(sessionID); failure != nil {
		// Every turn re-applies the selection: the adapter is host-wide, so a
		// later turn must not inherit whichever session ran last.
		return nil, failure
	}
	task := zenforge.Task{
		RunID: continuationID,
		Input: text,
		// Every turn carries the identity of the prompt that started it: the
		// console retires one echo per submission, so a continuation's prompt
		// needs its own identity just as the first turn's does (ADR 0111).
		PromptID:        strings.TrimSpace(requestID),
		InitialMessages: h.conversationMessages(ctx, runIDs),
	}
	if _, err := h.manager.Start(ctx, task); err != nil {
		return nil, startFailure(continuationID, err)
	}
	return map[string]any{"accepted": true}, nil
}

// applyModelSelection makes the session's chosen model the adapter the run about
// to start will use. A session that never chose one keeps the host's configured
// adapter, and a host with no selection store has nothing to apply.
func (h *Handler) applyModelSelection(sessionID string) *methodError {
	store := h.modelSelectionStore()
	if store == nil {
		return nil
	}
	if err := store.ApplyModelSelection(sessionID); err != nil {
		return fail(codeArgumentsInvalid,
			fmt.Sprintf("session %q has a model selection this host cannot apply: %s", sessionID, err.Error()),
			map[string]any{"sessionId": sessionID})
	}
	return nil
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
//
// The console stops the session it has open, whose id names the conversation's
// first turn, while a conversation of several turns runs its newest one under
// `<session>~<k>` (ADR 0108). Cancel therefore names the conversation's newest
// turn, not the id the caller happened to use: cancelling the first turn is a
// no-op that reports a conflict about a finished run while the run the operator
// is watching keeps going (ADR 0113).
func (h *Handler) sessionCancel(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	sessionID, _, failure := stringArg(args, "sessionId")
	if failure != nil {
		return nil, failure
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, argumentRequired("sessionId")
	}
	// A caller may name any turn of the conversation; the newest one is the turn
	// that can be running. A session with no turns yet (a draft) keeps the id it
	// was named by, which the manager answers as not-found.
	target := sessionID
	if runIDs := h.sessionRunIDs(ctx, sessionID); len(runIDs) > 0 {
		target = runIDs[len(runIDs)-1]
	}
	if err := h.manager.Cancel(target); err != nil {
		switch {
		case errors.Is(err, harnesshttp.ErrRunNotFound):
			return nil, fail(codeSessionNotFound, fmt.Sprintf("session %q not found", sessionID),
				map[string]any{"sessionId": sessionID})
		case errors.Is(err, harnesshttp.ErrRunTerminal):
			status := "terminal"
			if info, getErr := h.manager.Get(target); getErr == nil {
				status = string(info.Status)
			}
			return nil, fail(codeSessionConflict,
				fmt.Sprintf("session %q already finished with status %q; cancel is a no-op", target, status),
				map[string]any{"sessionId": sessionID, "turn": target, "status": status})
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

	// The page is the session's whole conversation, not the newest turn's log:
	// each turn's events are shifted past the turns before them so the sequence
	// the console cursors on never moves backwards (dshwire.Session). Reading
	// only the newest turn served a second turn's numbers from one, which the
	// console rejects as a stream resumed behind its last applied entry, and it
	// made an earlier turn unreachable through "load earlier".
	log, err := dshwire.Session(ctx, h, sessionID, func(turn int) dshwire.Identity {
		identity := h.wireIdentity(sessionID)
		identity.Turn = turn
		return identity
	})
	if err != nil {
		return nil, fail(codeInternal, "read session log: "+err.Error(), nil)
	}
	if len(log.Records) == 0 {
		// A session this host has created but no turn has started -- the draft the
		// console opens before its first prompt -- has an empty history, not a
		// missing one. The console loads a session's history the moment it opens
		// it, so answering not-found here is what the page reports as "Failed to
		// load history" on a brand-new chat. An id this host never created is
		// still not-found.
		if h.isPending(sessionID) {
			return map[string]any{"records": []any{}, "hasMore": false}, nil
		}
		return nil, fail(codeSessionNotFound, fmt.Sprintf("session %q not found", sessionID),
			map[string]any{"sessionId": sessionID})
	}
	window, hasMore := log.Through(throughSeq, beforeSeq, hasBefore, int(maxMessages))
	records := make([]map[string]any, 0, len(window))
	for _, record := range window {
		records = append(records, map[string]any{"type": "event", "event": record})
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

// wireIdentity names the provider and model the projected transcript attributes
// a session's assistant messages to. A session that chose a model shows the
// chosen one; a session that chose nothing runs on the host's configured model,
// which is what ModelDefault reports. The identity is provenance on the wire --
// the run itself is served by the adapter the selection path already applied --
// so an empty answer is legal and simply leaves the label unset.
func (h *Handler) wireIdentity(sessionID string) dshwire.Identity {
	if store := h.modelSelectionStore(); store != nil {
		if identity, ok := store.(sessionModelIdentity); ok {
			if provider, model, known := identity.SessionModelIdentity(sessionID); known {
				return dshwire.Identity{Provider: provider, Model: model}
			}
		}
	}
	if h.modelDefault != nil {
		return h.modelDefault()
	}
	return dshwire.Identity{}
}

// finishedRunMessage explains the prompt a finished session cannot take. It is
// the last resort: a session whose newest turn is finished starts the next turn
// of the conversation, so this message covers a run the console cannot continue.
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

// IsDraftSession reports whether this host created the session and no turn has
// started in it yet: the draft the console opens before its first prompt. Its
// history is empty rather than missing, which is what session/page answers and
// what the follow stream needs to know before it waits for the first turn
// instead of refusing a session it has no run for.
func (h *Handler) IsDraftSession(sessionID string) bool {
	return h.isPending(sessionID)
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
