package harnesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/server/sse"
)

// Agent is the subset of zenforge.Agent used by the HTTP adapter.
type Agent interface {
	Stream(ctx context.Context, task zenforge.Task) (<-chan zenforge.Event, error)
	Resume(ctx context.Context, runID string) (<-chan zenforge.Event, error)
}

// Handler exposes run and resume endpoints for an already configured agent.
type Handler struct {
	Agent Agent
	SSE   sse.Options
}

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
	events, err := h.Agent.Stream(r.Context(), zenforge.Task{
		RunID: req.RunID,
		Input: req.Input,
		Meta:  req.Meta,
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
	runID, ok := resumeRunID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "run_id_required", "runId is required")
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

func resumeRunID(r *http.Request) (string, bool) {
	if r.Method == http.MethodGet {
		runID := strings.TrimSpace(r.URL.Query().Get("runId"))
		return runID, runID != ""
	}
	var req ResumeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return "", false
	}
	runID := strings.TrimSpace(req.RunID)
	return runID, runID != ""
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	})
}
