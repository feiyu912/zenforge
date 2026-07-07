package harnesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/eventlog"
	"github.com/feiyu912/zenforge/server/sse"
)

// Agent is the subset of zenforge.Agent used by the HTTP adapter.
type Agent interface {
	Stream(ctx context.Context, task zenforge.Task) (<-chan zenforge.Event, error)
	Resume(ctx context.Context, runID string) (<-chan zenforge.Event, error)
}

// Handler exposes run, resume, and event replay endpoints for an already
// configured agent.
type Handler struct {
	Agent         Agent
	Manager       *RunManager
	Events        zenforge.EventStore
	Bus           *eventlog.Bus
	ApprovalInbox approval.Inbox
	Approvals     *approval.PendingBroker
	SSE           sse.Options
	LiveBuffer    int
	Access        AccessController
}

type Operation struct {
	Name  string
	RunID string
}

type AccessDecision struct {
	Meta map[string]any
}

type AccessController interface {
	Authorize(ctx context.Context, r *http.Request, operation Operation) (AccessDecision, error)
}

type AccessFunc func(ctx context.Context, r *http.Request, operation Operation) (AccessDecision, error)

func (f AccessFunc) Authorize(ctx context.Context, r *http.Request, operation Operation) (AccessDecision, error) {
	return f(ctx, r, operation)
}

var (
	ErrUnauthorized = errors.New("unauthorized")
	ErrForbidden    = errors.New("forbidden")
)

// RunRequest is the JSON body accepted by Handler.ServeRun.
type RunRequest struct {
	RunID string         `json:"runId,omitempty"`
	Input string         `json:"input"`
	Meta  map[string]any `json:"meta,omitempty"`
}

// ResumeRequest is the JSON body accepted by Handler.ServeResume for POST.
type ResumeRequest struct {
	RunID string `json:"runId"`
}

// ApprovalRequest is the JSON body accepted by Handler.ServeApproval.
type ApprovalRequest struct {
	RequestID string                  `json:"requestId"`
	Action    approval.DecisionAction `json:"action"`
	Scope     approval.DecisionScope  `json:"scope,omitempty"`
	Reason    string                  `json:"reason,omitempty"`
	Payload   map[string]any          `json:"payload,omitempty"`
}

// New creates a Handler that streams agent events as Server-Sent Events.
func New(agent Agent, opts sse.Options) *Handler {
	return &Handler{Agent: agent, SSE: opts}
}

// ServeRun accepts a JSON RunRequest and streams the resulting agent events.
func (h *Handler) ServeRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "run requires POST")
		return
	}
	if h.Agent == nil {
		writeError(w, http.StatusInternalServerError, "agent_not_configured", "agent is not configured")
		return
	}
	var req RunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	req.Input = strings.TrimSpace(req.Input)
	if req.Input == "" {
		writeError(w, http.StatusBadRequest, "input_required", "input is required")
		return
	}
	decision, ok := h.authorize(w, r, Operation{Name: "run", RunID: req.RunID})
	if !ok {
		return
	}
	events, err := h.Agent.Stream(r.Context(), zenforge.Task{
		RunID: req.RunID,
		Input: req.Input,
		Meta:  mergeMeta(req.Meta, decision.Meta),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "run_failed", err.Error())
		return
	}
	if err := sse.StreamHTTP(r.Context(), w, events, h.SSE); err != nil && !errors.Is(err, context.Canceled) {
		writeError(w, http.StatusInternalServerError, "stream_failed", err.Error())
	}
}

// ServeResume resumes a run by runId and streams the resulting agent events.
func (h *Handler) ServeResume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "resume requires GET or POST")
		return
	}
	if h.Agent == nil {
		writeError(w, http.StatusInternalServerError, "agent_not_configured", "agent is not configured")
		return
	}
	runID, err := resumeRunID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if runID == "" {
		writeError(w, http.StatusBadRequest, "run_id_required", "runId is required")
		return
	}
	if _, ok := h.authorize(w, r, Operation{Name: "resume", RunID: runID}); !ok {
		return
	}
	events, err := h.Agent.Resume(r.Context(), runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "resume_failed", err.Error())
		return
	}
	if err := sse.StreamHTTP(r.Context(), w, events, h.SSE); err != nil && !errors.Is(err, context.Canceled) {
		writeError(w, http.StatusInternalServerError, "stream_failed", err.Error())
	}
}

// ServeDetachedStart starts a run whose lifetime is owned by the configured manager.
func (h *Handler) ServeDetachedStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "detached start requires POST")
		return
	}
	if h.Manager == nil {
		writeError(w, http.StatusServiceUnavailable, "manager_not_configured", "run manager is not configured")
		return
	}
	var req RunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	req.RunID = strings.TrimSpace(req.RunID)
	req.Input = strings.TrimSpace(req.Input)
	if req.Input == "" {
		writeError(w, http.StatusBadRequest, "input_required", "input is required")
		return
	}
	decision, ok := h.authorize(w, r, Operation{Name: "detachedStart", RunID: req.RunID})
	if !ok {
		return
	}
	info, err := h.Manager.Start(r.Context(), zenforge.Task{
		RunID: req.RunID,
		Input: req.Input,
		Meta:  mergeMeta(req.Meta, decision.Meta),
	})
	if err != nil {
		writeManagerError(w, "detached_start_failed", err)
		return
	}
	writeJSON(w, http.StatusAccepted, info)
}

// ServeDetachedResume resumes a durable run under the configured manager.
func (h *Handler) ServeDetachedResume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "detached resume requires GET or POST")
		return
	}
	if h.Manager == nil {
		writeError(w, http.StatusServiceUnavailable, "manager_not_configured", "run manager is not configured")
		return
	}
	runID, err := resumeRunID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if runID == "" {
		writeError(w, http.StatusBadRequest, "run_id_required", "runId is required")
		return
	}
	if _, ok := h.authorize(w, r, Operation{Name: "detachedResume", RunID: runID}); !ok {
		return
	}
	info, err := h.Manager.Resume(r.Context(), runID)
	if err != nil {
		writeManagerError(w, "detached_resume_failed", err)
		return
	}
	writeJSON(w, http.StatusAccepted, info)
}

// ServeDetachedStatus returns the manager's current snapshot for a run.
func (h *Handler) ServeDetachedStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "detached status requires GET")
		return
	}
	if h.Manager == nil {
		writeError(w, http.StatusServiceUnavailable, "manager_not_configured", "run manager is not configured")
		return
	}
	runID := strings.TrimSpace(r.URL.Query().Get("runId"))
	if runID == "" {
		writeError(w, http.StatusBadRequest, "run_id_required", "runId is required")
		return
	}
	if _, ok := h.authorize(w, r, Operation{Name: "detachedStatus", RunID: runID}); !ok {
		return
	}
	info, err := h.Manager.Get(runID)
	if err != nil {
		writeManagerError(w, "detached_status_failed", err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// ServeDetachedAttach replays durable events and follows the run until completion.
func (h *Handler) ServeDetachedAttach(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "detached attach requires GET")
		return
	}
	if h.Manager == nil {
		writeError(w, http.StatusServiceUnavailable, "manager_not_configured", "run manager is not configured")
		return
	}
	runID := strings.TrimSpace(r.URL.Query().Get("runId"))
	if runID == "" {
		writeError(w, http.StatusBadRequest, "run_id_required", "runId is required")
		return
	}
	if _, ok := h.authorize(w, r, Operation{Name: "detachedAttach", RunID: runID}); !ok {
		return
	}
	afterSeq, err := int64Query(r, "afterSeq")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_after_seq", err.Error())
		return
	}
	if _, present := r.URL.Query()["afterSeq"]; !present {
		afterSeq, err = lastEventID(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_last_event_id", err.Error())
			return
		}
	}
	attachCtx, cancel := context.WithCancel(r.Context())
	events, errs, err := h.Manager.Attach(attachCtx, runID, afterSeq)
	if err != nil {
		cancel()
		writeManagerError(w, "detached_attach_failed", err)
		return
	}
	streamErr := sse.StreamHTTP(r.Context(), w, events, h.SSE)
	cancel()
	followErr := <-errs
	if streamErr != nil && !errors.Is(streamErr, context.Canceled) {
		writeError(w, http.StatusInternalServerError, "stream_failed", streamErr.Error())
		return
	}
	if followErr != nil && !errors.Is(followErr, context.Canceled) {
		writeError(w, http.StatusInternalServerError, "detached_attach_failed", followErr.Error())
	}
}

// ServeDetachedCancel explicitly cancels an active detached run.
func (h *Handler) ServeDetachedCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "detached cancel requires POST or DELETE")
		return
	}
	if h.Manager == nil {
		writeError(w, http.StatusServiceUnavailable, "manager_not_configured", "run manager is not configured")
		return
	}
	runID, err := cancelRunID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if runID == "" {
		writeError(w, http.StatusBadRequest, "run_id_required", "runId is required")
		return
	}
	if _, ok := h.authorize(w, r, Operation{Name: "detachedCancel", RunID: runID}); !ok {
		return
	}
	if err := h.Manager.Cancel(runID); err != nil {
		writeManagerError(w, "detached_cancel_failed", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"runId": runID})
}

// ServeEvents replays persisted events for a run as Server-Sent Events.
func (h *Handler) ServeEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "events requires GET")
		return
	}
	if h.Events == nil {
		writeError(w, http.StatusInternalServerError, "event_store_not_configured", "event store is not configured")
		return
	}
	runID := strings.TrimSpace(r.URL.Query().Get("runId"))
	if runID == "" {
		writeError(w, http.StatusBadRequest, "run_id_required", "runId is required")
		return
	}
	if _, ok := h.authorize(w, r, Operation{Name: "events", RunID: runID}); !ok {
		return
	}
	afterSeq, err := int64Query(r, "afterSeq")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_after_seq", err.Error())
		return
	}
	limit, err := intQuery(r, "limit")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_limit", err.Error())
		return
	}
	events, err := h.Events.Read(r.Context(), runID, afterSeq, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "events_failed", err.Error())
		return
	}
	if err := sse.StreamHTTP(r.Context(), w, sliceEvents(events), h.SSE); err != nil && !errors.Is(err, context.Canceled) {
		writeError(w, http.StatusInternalServerError, "stream_failed", err.Error())
	}
}

// ServeLiveEvents streams live events for a run from the configured event bus.
func (h *Handler) ServeLiveEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "live events requires GET")
		return
	}
	if h.Bus == nil {
		writeError(w, http.StatusInternalServerError, "event_bus_not_configured", "event bus is not configured")
		return
	}
	runID := strings.TrimSpace(r.URL.Query().Get("runId"))
	if runID == "" {
		writeError(w, http.StatusBadRequest, "run_id_required", "runId is required")
		return
	}
	if _, ok := h.authorize(w, r, Operation{Name: "liveEvents", RunID: runID}); !ok {
		return
	}
	replay, err := boolQuery(r, "replay")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_replay", err.Error())
		return
	}
	buffer := h.LiveBuffer
	if buffer == 0 {
		buffer = 128
	}
	if buffer < 0 {
		writeError(w, http.StatusInternalServerError, "invalid_live_buffer", "live event buffer must be non-negative")
		return
	}
	if replay {
		if h.Events == nil {
			writeError(w, http.StatusInternalServerError, "event_store_not_configured", "event store is not configured")
			return
		}
		afterSeq, err := int64Query(r, "afterSeq")
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_after_seq", err.Error())
			return
		}
		if _, present := r.URL.Query()["afterSeq"]; !present {
			afterSeq, err = lastEventID(r)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_last_event_id", err.Error())
				return
			}
		}
		followCtx, cancel := context.WithCancel(r.Context())
		events, errs, err := eventlog.Follow(followCtx, h.Events, h.Bus, runID, afterSeq, eventlog.FollowOptions{
			LiveBuffer: buffer,
		})
		if err != nil {
			cancel()
			writeError(w, http.StatusBadRequest, "live_events_failed", err.Error())
			return
		}
		_ = sse.StreamHTTP(r.Context(), w, events, h.SSE)
		cancel()
		<-errs
		return
	}
	events, unsubscribe, err := h.Bus.Subscribe(r.Context(), runID, buffer)
	if err != nil {
		writeError(w, http.StatusBadRequest, "live_events_failed", err.Error())
		return
	}
	defer unsubscribe()
	if err := sse.StreamHTTP(r.Context(), w, events, h.SSE); err != nil && !errors.Is(err, context.Canceled) {
		writeError(w, http.StatusInternalServerError, "stream_failed", err.Error())
	}
}

// ServeApproval submits a decision to a pending approval request.
func (h *Handler) ServeApproval(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "approval requires POST")
		return
	}
	inbox := h.approvalInbox()
	if inbox == nil {
		writeError(w, http.StatusInternalServerError, "approval_broker_not_configured", "approval broker is not configured")
		return
	}
	var req ApprovalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	decision := approval.Decision{
		RequestID: strings.TrimSpace(req.RequestID),
		Action:    req.Action,
		Scope:     req.Scope,
		Reason:    req.Reason,
		Payload:   req.Payload,
	}
	if err := decision.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_approval", err.Error())
		return
	}
	if decision.Scope == "" {
		decision.Scope = approval.ScopeOnce
	}
	if decision.DecidedAt.IsZero() {
		decision.DecidedAt = time.Now().UTC()
	}
	pending, err := inbox.Lookup(r.Context(), decision.RequestID)
	if errors.Is(err, approval.ErrRequestNotFound) {
		writeError(w, http.StatusNotFound, "approval_not_found", approval.ErrRequestNotFound.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "approval_lookup_failed", err.Error())
		return
	}
	if _, ok := h.authorize(w, r, Operation{Name: "approval", RunID: pending.RunID}); !ok {
		return
	}
	if err := inbox.Submit(r.Context(), decision); err != nil {
		if errors.Is(err, approval.ErrRequestNotFound) {
			writeError(w, http.StatusNotFound, "approval_not_found", err.Error())
			return
		}
		if errors.Is(err, approval.ErrDecisionConflict) || errors.Is(err, approval.ErrRequestExpired) {
			writeError(w, http.StatusConflict, "approval_conflict", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "approval_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"decision": decision,
	})
}

// ServeApprovals lists pending approval requests for one run.
func (h *Handler) ServeApprovals(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "approvals requires GET")
		return
	}
	inbox := h.approvalInbox()
	if inbox == nil {
		writeError(w, http.StatusInternalServerError, "approval_broker_not_configured", "approval broker is not configured")
		return
	}
	runID := strings.TrimSpace(r.URL.Query().Get("runId"))
	if runID == "" {
		writeError(w, http.StatusBadRequest, "run_id_required", "runId is required")
		return
	}
	if _, ok := h.authorize(w, r, Operation{Name: "approvals", RunID: runID}); !ok {
		return
	}
	pending, err := inbox.List(r.Context(), runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "approvals_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"approvals": pending})
}

func (h *Handler) approvalInbox() approval.Inbox {
	if h.ApprovalInbox != nil && !nilInterface(h.ApprovalInbox) {
		return h.ApprovalInbox
	}
	if h.Approvals != nil {
		return h.Approvals
	}
	return nil
}

func (h *Handler) authorize(w http.ResponseWriter, r *http.Request, operation Operation) (AccessDecision, bool) {
	if h.Access == nil {
		return AccessDecision{}, true
	}
	decision, err := h.Access.Authorize(r.Context(), r, operation)
	if err == nil {
		return decision, true
	}
	switch {
	case errors.Is(err, ErrUnauthorized):
		writeError(w, http.StatusUnauthorized, "unauthorized", err.Error())
	case errors.Is(err, ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "access_failed", err.Error())
	}
	return AccessDecision{}, false
}

func mergeMeta(client, trusted map[string]any) map[string]any {
	if client == nil && trusted == nil {
		return nil
	}
	out := make(map[string]any, len(client)+len(trusted))
	for key, value := range client {
		out[key] = value
	}
	for key, value := range trusted {
		out[key] = value
	}
	return out
}

func resumeRunID(r *http.Request) (string, error) {
	if r.Method == http.MethodGet {
		runID := strings.TrimSpace(r.URL.Query().Get("runId"))
		return runID, nil
	}
	var req ResumeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return "", err
	}
	runID := strings.TrimSpace(req.RunID)
	return runID, nil
}

func cancelRunID(r *http.Request) (string, error) {
	if runID := strings.TrimSpace(r.URL.Query().Get("runId")); runID != "" {
		return runID, nil
	}
	if r.Method == http.MethodDelete {
		return "", nil
	}
	var req ResumeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return "", err
	}
	return strings.TrimSpace(req.RunID), nil
}

func int64Query(r *http.Request, key string) (int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, err
	}
	if value < 0 {
		return 0, errors.New(key + " must be non-negative")
	}
	return value, nil
}

func intQuery(r *http.Request, key string) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, err
	}
	if value < 0 {
		return 0, errors.New(key + " must be non-negative")
	}
	return value, nil
}

func boolQuery(r *http.Request, key string) (bool, error) {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return false, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean", key)
	}
	return parsed, nil
}

func lastEventID(r *http.Request) (int64, error) {
	value := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if value == "" {
		return 0, nil
	}
	seq, err := strconv.ParseInt(value, 10, 64)
	if err != nil || seq < 0 {
		return 0, fmt.Errorf("Last-Event-ID must be a non-negative integer")
	}
	return seq, nil
}

func sliceEvents(events []zenforge.Event) <-chan zenforge.Event {
	out := make(chan zenforge.Event, len(events))
	for _, event := range events {
		out <- event
	}
	close(out)
	return out
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	if message == "" {
		message = fmt.Sprintf("http status %d", status)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	})
}

func writeManagerError(w http.ResponseWriter, code string, err error) {
	status := http.StatusServiceUnavailable
	switch {
	case errors.Is(err, ErrInvalidRunID):
		status = http.StatusBadRequest
	case errors.Is(err, ErrRunNotFound), errors.Is(err, ErrResumeNotFound):
		status = http.StatusNotFound
	case errors.Is(err, ErrRunExists), errors.Is(err, ErrRunTerminal), errors.Is(err, ErrRunActive):
		status = http.StatusConflict
	case errors.Is(err, ErrMaxActive):
		status = http.StatusTooManyRequests
	case errors.Is(err, ErrManagerClosed), errors.Is(err, ErrEventsRequired):
		status = http.StatusServiceUnavailable
	}
	writeError(w, status, code, err.Error())
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
