// Package hooks runs user-configured commands at lifecycle points and lets
// them observe, annotate, or refuse what the agent is about to do. It ports
// the reference's hook contract: a hook receives a JSON payload on stdin
// describing the event, its exit code selects the outcome (0 parses stdout
// as a JSON decision, 2 blocks with stderr as the reason, anything else is a
// failure), and the JSON decision can continue or stop processing, add
// context, or rewrite a tool's arguments.
//
// The engine is deliberately independent of the agent: it takes a request
// and returns an outcome, so it is testable without a model, and the
// middleware in middleware.go is the only place that couples it to tool
// execution.
package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Event is a lifecycle point a hook can run at.
type Event string

// The events this package dispatches. They are the reference's names.
const (
	// EventSessionStart runs once when a run begins.
	EventSessionStart Event = "SessionStart"
	// EventUserPromptSubmit runs when a task is submitted, before the model
	// sees it.
	EventUserPromptSubmit Event = "UserPromptSubmit"
	// EventPreToolUse runs before a tool call and may block or rewrite it.
	EventPreToolUse Event = "PreToolUse"
	// EventPostToolUse runs after a tool call and may add context or ask for
	// the failure to be reconsidered.
	EventPostToolUse Event = "PostToolUse"
	// EventStop runs when the agent is about to finish and may block that.
	EventStop Event = "Stop"
)

// Events lists every supported event.
var Events = []Event{EventSessionStart, EventUserPromptSubmit, EventPreToolUse, EventPostToolUse, EventStop}

// EventsWithMatchers are the events whose matcher is meaningful, because
// they dispatch against a concrete tool name.
var EventsWithMatchers = []Event{EventPreToolUse, EventPostToolUse}

// ParseEvent resolves an event name. Configuration is written by hand, so
// the comparison ignores case and separators: `PreToolUse`, `pre_tool_use`,
// `pre-tool-use`, and `pretooluse` all name the same event.
func ParseEvent(name string) (Event, error) {
	normalized := normalizeEventName(name)
	for _, event := range Events {
		if normalizeEventName(string(event)) == normalized {
			return event, nil
		}
	}
	return "", fmt.Errorf("unknown hook event %q (want %s)", name, strings.Join(eventNames(), ", "))
}

// normalizeEventName lowercases and strips separators.
func normalizeEventName(name string) string {
	trimmed := strings.ToLower(strings.TrimSpace(name))
	trimmed = strings.ReplaceAll(trimmed, "_", "")
	return strings.ReplaceAll(trimmed, "-", "")
}

func eventNames() []string {
	names := make([]string, 0, len(Events))
	for _, event := range Events {
		names = append(names, string(event))
	}
	return names
}

// Command is one configured hook.
type Command struct {
	// Matcher restricts the hook to tool names. It is a regular expression
	// matched against the tool name for the events that dispatch against a
	// tool; empty matches everything, and it is ignored for other events.
	Matcher string `json:"matcher,omitempty"`
	// Command is the shell command line to run. Required.
	Command string `json:"command"`
	// Description is for humans and appears in the session metadata.
	Description string `json:"description,omitempty"`
	// Timeout bounds one run. Zero uses the engine default.
	Timeout time.Duration `json:"timeout,omitempty"`
	// FailClosed turns a hook failure (a crash, a timeout, an unparsable
	// decision) into a block. The reference treats a failure as a warning
	// and continues, so this is opt-in per hook.
	FailClosed bool `json:"failClosed,omitempty"`
}

// UnmarshalJSON accepts the timeout as either a duration string ("5s",
// matching the rest of the configuration) or a number of nanoseconds.
func (c *Command) UnmarshalJSON(data []byte) error {
	type alias Command
	aux := struct {
		*alias
		Timeout any `json:"timeout,omitempty"`
	}{alias: (*alias)(c)}
	// A custom unmarshaler must re-impose strictness: decoding through the
	// outer decoder's DisallowUnknownFields does not reach this object, and
	// a silently ignored field in a hook declaration is a hook the user
	// believes is in force.
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&aux); err != nil {
		return err
	}
	c.Timeout = 0
	switch value := aux.Timeout.(type) {
	case nil:
	case string:
		parsed, err := time.ParseDuration(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("parse hook timeout %q: %w", value, err)
		}
		c.Timeout = parsed
	case float64:
		c.Timeout = time.Duration(value)
	default:
		return fmt.Errorf("hook timeout must be a duration string or a number")
	}
	return nil
}

// MarshalJSON writes the timeout as a duration string.
func (c Command) MarshalJSON() ([]byte, error) {
	type alias Command
	aux := struct {
		alias
		Timeout string `json:"timeout,omitempty"`
	}{alias: alias(c)}
	if c.Timeout > 0 {
		aux.Timeout = c.Timeout.String()
	}
	return json.Marshal(aux)
}

// Config is a set of hooks per event.
type Config map[Event][]Command

// DefaultTimeout bounds one hook run.
const DefaultTimeout = 30 * time.Second

// DefaultMaxOutputBytes bounds what one hook may capture.
const DefaultMaxOutputBytes = 64 << 10

// Validate rejects a configuration the engine cannot honour.
func (c Config) Validate() error {
	for event, commands := range c {
		if _, err := ParseEvent(string(event)); err != nil {
			return err
		}
		if len(commands) == 0 {
			return fmt.Errorf("hook event %s has no commands", event)
		}
		for index, command := range commands {
			if strings.TrimSpace(command.Command) == "" {
				return fmt.Errorf("%s hook %d has an empty command", event, index)
			}
			if command.Matcher != "" && !hasMatcher(event) {
				return fmt.Errorf("%s hook %d sets a matcher, which only %s hooks use", event, index, eventNamesWithMatchers())
			}
			if command.Timeout < 0 {
				return fmt.Errorf("%s hook %d has a negative timeout", event, index)
			}
		}
	}
	return nil
}

func hasMatcher(event Event) bool {
	for _, candidate := range EventsWithMatchers {
		if candidate == event {
			return true
		}
	}
	return false
}

func eventNamesWithMatchers() string {
	names := make([]string, 0, len(EventsWithMatchers))
	for _, event := range EventsWithMatchers {
		names = append(names, string(event))
	}
	return strings.Join(names, " and ")
}

// Request is one hook dispatch.
type Request struct {
	// Event is the lifecycle point.
	Event Event
	// SessionID and RunID identify the run.
	SessionID string
	RunID     string
	// CWD is the working directory the hook runs in.
	CWD string
	// ToolName and ToolInput describe the tool call for the tool events.
	ToolName  string
	ToolInput map[string]any
	// ToolOutput is the tool's output for PostToolUse.
	ToolOutput string
	// ToolError is the tool's error for PostToolUse.
	ToolError string
	// Prompt is the submitted task for UserPromptSubmit.
	Prompt string
	// StopReason is why the agent intends to stop, for Stop.
	StopReason string
}

// Status is how one hook run ended.
type Status string

const (
	// StatusSuccess means the hook ran and its decision was applied.
	StatusSuccess Status = "success"
	// StatusBlocked means the hook blocked the action.
	StatusBlocked Status = "blocked"
	// StatusFailed means the hook could not produce a decision.
	StatusFailed Status = "failed"
	// StatusSkipped means the hook did not match.
	StatusSkipped Status = "skipped"
)

// Entry is the outcome of one configured hook.
type Entry struct {
	Command string `json:"command"`
	// Matcher is the configured matcher, echoed for diagnostics.
	Matcher string `json:"matcher,omitempty"`
	Status  Status `json:"status"`
	// ExitCode is the hook's exit code, when it ran.
	ExitCode *int `json:"exitCode,omitempty"`
	// Reason explains a block or a failure.
	Reason string `json:"reason,omitempty"`
	// Feedback is human-readable output the hook produced on stdout.
	Feedback string `json:"feedback,omitempty"`
	// Stderr is what the hook wrote to stderr.
	Stderr string `json:"stderr,omitempty"`
	// Duration is how long the hook took.
	Duration time.Duration `json:"duration"`
	// TimedOut reports a hook killed by its timeout.
	TimedOut bool `json:"timedOut,omitempty"`
}

// Outcome is the aggregate decision of every matching hook.
type Outcome struct {
	// Event is the event that was dispatched.
	Event Event `json:"event"`
	// Entries are the per-hook results, in configuration order.
	Entries []Entry `json:"entries,omitempty"`
	// Blocked is true when any matching hook blocked the action.
	Blocked bool `json:"blocked"`
	// Reason explains the block.
	Reason string `json:"reason,omitempty"`
	// Continue follows the reference's `continue` field: false asks the
	// agent to stop processing.
	Continue bool `json:"continue"`
	// StopReason is the reason attached to Continue: false.
	StopReason string `json:"stopReason,omitempty"`
	// SystemMessage is a message the hook wants the user to see.
	SystemMessage string `json:"systemMessage,omitempty"`
	// SuppressOutput asks the runtime not to show the hook's output.
	SuppressOutput bool `json:"suppressOutput,omitempty"`
	// AdditionalContext is context the hook injects into the conversation.
	AdditionalContext string `json:"additionalContext,omitempty"`
	// UpdatedInput replaces a tool call's arguments when a PreToolUse hook
	// supplies it.
	UpdatedInput map[string]any `json:"updatedInput,omitempty"`
}

// Blocked reports whether the action must not proceed, and why.
func (o Outcome) BlockMessage() string {
	if !o.Blocked {
		return ""
	}
	if strings.TrimSpace(o.Reason) != "" {
		return o.Reason
	}
	return "blocked by a hook"
}

// Failed reports the hooks that failed, so a caller can surface them.
func (o Outcome) Failed() []Entry {
	failed := make([]Entry, 0, len(o.Entries))
	for _, entry := range o.Entries {
		if entry.Status == StatusFailed {
			failed = append(failed, entry)
		}
	}
	return failed
}

// Summary renders the outcome for the model and the log.
func (o Outcome) Summary() string {
	lines := make([]string, 0, len(o.Entries)+1)
	for _, entry := range o.Entries {
		switch entry.Status {
		case StatusBlocked:
			lines = append(lines, fmt.Sprintf("hook blocked (%s): %s", entry.Command, entry.Reason))
		case StatusFailed:
			lines = append(lines, fmt.Sprintf("hook failed (%s): %s", entry.Command, entry.Reason))
		case StatusSuccess:
			if trimmed := strings.TrimSpace(entry.Feedback); trimmed != "" {
				lines = append(lines, fmt.Sprintf("hook feedback (%s): %s", entry.Command, trimmed))
			}
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}
