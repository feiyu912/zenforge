package memory

import (
	"encoding/json"
	"strings"
)

// RunDigest accumulates what a finished run is worth remembering from the
// events it emitted. It is deliberately lossy: memory distillation wants the
// shape of the run (what was attempted, what failed, what changed), not a
// transcript, and copying the whole event log into a model call would cost
// more than the memory is worth.
type RunDigest struct {
	Task     string
	Output   string
	Commands []string
	Failures []string
	Files    []string
	Events   []string
}

// MaxDigestEvents bounds the rendered event trail.
const MaxDigestEvents = 200

// maxDigestList bounds each collected list.
const maxDigestList = 100

// NewRunDigest starts a digest for a task.
func NewRunDigest(task string) *RunDigest {
	return &RunDigest{Task: task}
}

// Observe folds one run event into the digest. Unknown events are counted in
// the trail but not interpreted, so a new event type cannot break memory.
func (d *RunDigest) Observe(eventType string, payload map[string]any) {
	if d == nil {
		return
	}
	switch eventType {
	case "run.done":
		d.Output = stringField(payload, "output", d.Output)
	case "tool.call":
		if command := commandFromArguments(payload["arguments"]); command != "" {
			d.Commands = appendBounded(d.Commands, command)
		}
	case "tool.error":
		message := stringField(payload, "error", "")
		if message == "" {
			message = stringField(payload, "message", "")
		}
		if message != "" {
			d.Failures = appendBounded(d.Failures, message)
		}
	case "workspace.changed":
		if path := stringField(payload, "path", ""); path != "" {
			d.Files = appendBounded(d.Files, path)
		}
	}
	if len(d.Events) < MaxDigestEvents {
		d.Events = append(d.Events, eventType)
	}
}

// Summary renders the digest for a distiller.
func (d *RunDigest) Summary(runID, project string) RunSummary {
	if d == nil {
		return RunSummary{RunID: runID, Project: project}
	}
	return RunSummary{
		RunID:    runID,
		Project:  project,
		Task:     d.Task,
		Output:   d.Output,
		Commands: d.Commands,
		Failures: d.Failures,
		Files:    dedupe(d.Files),
		Events:   strings.Join(d.Events, " "),
	}
}

// commandFromArguments extracts a shell command from a tool call's
// arguments. The key names cover this project's shell tool and the
// reference's exec tools; anything else is ignored rather than guessed at.
func commandFromArguments(raw any) string {
	switch value := raw.(type) {
	case map[string]any:
		for _, key := range []string{"command", "cmd", "script"} {
			if text, ok := value[key].(string); ok && strings.TrimSpace(text) != "" {
				return text
			}
		}
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return ""
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(trimmed), &decoded); err == nil {
			return commandFromArguments(decoded)
		}
	case json.RawMessage:
		return commandFromArguments(string(value))
	}
	return ""
}

func stringField(payload map[string]any, key, fallback string) string {
	if payload == nil {
		return fallback
	}
	if text, ok := payload[key].(string); ok {
		return text
	}
	return fallback
}

func appendBounded(values []string, value string) []string {
	if len(values) >= maxDigestList {
		return values
	}
	return append(values, value)
}

func dedupe(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
