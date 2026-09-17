// Package askuser implements the DSH ask_user_question tool: the model
// asks the human one or more concise questions, each with a stable id
// echoed back in the answer, optionally with structured choices.
//
// The tool is durable like every other approval-gated zenforge tool: a
// call without an answer yet returns an approval request
// (operation user.question) and pauses the run; any broker — the
// interactive CLI, the server inbox, or a GUI — decides it, and the
// answers travel back in approval.Decision.Payload["answers"] keyed by
// question id. The retried call renders those answers as its tool
// result, so resume replays the exact exchange.
//
// Following DSH's root-agent-only rule, subagents cannot ask the user:
// their calls fail with a recoverable error telling them to return the
// question in their final report so the parent can ask instead.
package askuser

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/tool"
	"github.com/feiyu912/zenforge/tool/jsonschema"
)

// DefaultMaxQuestions caps one call, matching the DSH guidance that the
// model batches its missing information into a few concise questions.
const DefaultMaxQuestions = 4

// AnswersPayloadKey is the key a decider must use in
// approval.Decision.Payload to deliver answers: a map from question id
// to a string, or to a slice of strings for multi-select questions.
const AnswersPayloadKey = "answers"

// Operation is the approval request operation for question rounds.
const Operation = "user.question"

// Config configures the ask_user tool.
type Config struct {
	// MaxQuestions caps the questions accepted per call; zero selects
	// DefaultMaxQuestions.
	MaxQuestions int
}

// Option is one selectable choice for a question.
type Option struct {
	Label       string `json:"label" jsonschema:"required,description=Short user-facing option label"`
	Description string `json:"description,omitempty" jsonschema:"description=One-sentence explanation of the tradeoff or impact"`
}

// Question is a single question with a stable id echoed in the answer.
type Question struct {
	ID          string   `json:"id" jsonschema:"required,description=Stable id for this question; echoed in the answer"`
	Question    string   `json:"question" jsonschema:"required,description=The specific question to ask the user"`
	Header      string   `json:"header,omitempty" jsonschema:"description=Optional short heading for the question"`
	Options     []Option `json:"options,omitempty" jsonschema:"description=Optional choices to show the user; put a recommended one first and append (Recommended) to its label"`
	MultiSelect bool     `json:"multi_select,omitempty" jsonschema:"description=Whether the user may select more than one option; defaults to false"`
}

type input struct {
	Questions []Question `json:"questions" jsonschema:"required,description=Questions to ask the user before continuing"`
}

type askUserTool struct {
	config Config
	schema map[string]any
}

// New builds the ask_user tool.
func New(config Config) (tool.Tool, error) {
	if config.MaxQuestions < 0 {
		return nil, fmt.Errorf("askuser: maxQuestions must be non-negative")
	}
	return askUserTool{config: config, schema: jsonschema.Infer(input{})}, nil
}

// Must builds the ask_user tool and panics on configuration errors.
func Must(config Config) tool.Tool {
	askTool, err := New(config)
	if err != nil {
		panic(err)
	}
	return askTool
}

func (t askUserTool) Name() string {
	return "ask_user"
}

func (t askUserTool) Description() string {
	return "Ask the user one or more concise questions when you need confirmation, a choice, or missing information before proceeding. Each answer echoes the question id; the run pauses until the user responds."
}

func (t askUserTool) Schema() map[string]any {
	return t.schema
}

func (t askUserTool) Call(ctx context.Context, raw json.RawMessage, call tool.Context) (tool.Result, error) {
	var in input
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if len(raw) == 0 {
		decoder = json.NewDecoder(strings.NewReader(`{}`))
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(&in); err != nil {
		return tool.Result{Error: tool.ErrInvalidArguments.Error(), ExitCode: 1}, fmt.Errorf("%w: %v", tool.ErrInvalidArguments, err)
	}
	if err := validateQuestions(in.Questions, t.maxQuestions()); err != nil {
		return tool.Result{Error: err.Error(), ExitCode: 1}, err
	}
	if subtaskID, _ := call.Metadata["subtaskId"].(string); subtaskID != "" {
		// DSH root-agent-only rule: a delegated caller has no human on
		// the other side, and its questions must surface through the
		// parent's report instead.
		message := "ask_user is not available inside subagents; return the question in your final report so the parent agent can ask the user"
		return tool.Result{Error: message, ExitCode: 1}, fmt.Errorf("%w: %s", tool.ErrInvalidArguments, message)
	}

	fingerprint := questionsFingerprint(in.Questions)
	if approval.MatchesApprovedMetadata(call.Metadata, fingerprint, "") {
		answers := answersFromMetadata(call.Metadata)
		rendered := renderAnswers(answers)
		return tool.Result{
			Output:     rendered,
			Structured: map[string]any{"answers": answers, "message": rendered},
		}, nil
	}

	request := approval.RequiredPlan(approval.Request{
		ID:          approval.NewRequestID(call.RunID, call.ToolCallID, "ask_user"),
		RunID:       call.RunID,
		ToolCallID:  call.ToolCallID,
		ToolName:    "ask_user",
		Operation:   Operation,
		Title:       "Answer the agent's questions",
		Description: summarizeQuestions(in.Questions),
		Risk:        approval.RiskLow,
		Options:     approval.DefaultOptions(),
		Payload: map[string]any{
			"questions":   in.Questions,
			"fingerprint": fingerprint,
			"answersKey":  AnswersPayloadKey,
		},
		CreatedAt: time.Now().UTC(),
	}).Request
	return approval.RequiredResult(request), approval.ErrRequired
}

func (t askUserTool) maxQuestions() int {
	if t.config.MaxQuestions > 0 {
		return t.config.MaxQuestions
	}
	return DefaultMaxQuestions
}

func validateQuestions(questions []Question, max int) error {
	if len(questions) == 0 {
		return fmt.Errorf("%w: at least one question is required", tool.ErrInvalidArguments)
	}
	if len(questions) > max {
		return fmt.Errorf("%w: %d questions exceed the per-call limit of %d", tool.ErrInvalidArguments, len(questions), max)
	}
	seen := make(map[string]struct{}, len(questions))
	for _, question := range questions {
		id := strings.TrimSpace(question.ID)
		if id == "" {
			return fmt.Errorf("%w: every question needs a stable non-empty id", tool.ErrInvalidArguments)
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("%w: duplicate question id %q", tool.ErrInvalidArguments, id)
		}
		seen[id] = struct{}{}
		if strings.TrimSpace(question.Question) == "" {
			return fmt.Errorf("%w: question %q has empty question text", tool.ErrInvalidArguments, id)
		}
		for _, option := range question.Options {
			if strings.TrimSpace(option.Label) == "" {
				return fmt.Errorf("%w: question %q has an option with an empty label", tool.ErrInvalidArguments, id)
			}
		}
	}
	return nil
}

// questionsFingerprint hashes the canonical encoding of the question
// round so an identical re-ask after resume matches the persisted
// approval instead of prompting again.
func questionsFingerprint(questions []Question) string {
	encoded, err := json.Marshal(struct {
		Version   string     `json:"version"`
		Questions []Question `json:"questions"`
	}{Version: "zenforge.askuser.v1", Questions: questions})
	if err != nil {
		// Marshal of plain data structs cannot fail; fall back to a
		// value that never matches a persisted grant.
		return "askuser-invalid"
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// answersFromMetadata extracts the decider's answers from the approved
// call metadata. Missing or malformed payloads yield an empty map — the
// approval was granted, the human simply provided no answers.
func answersFromMetadata(metadata map[string]any) map[string]any {
	payload, _ := metadata[approval.MetadataDecisionPayload].(map[string]any)
	answers, _ := payload[AnswersPayloadKey].(map[string]any)
	if answers == nil {
		return map[string]any{}
	}
	return answers
}

// renderAnswers echoes the stable ids, matching the DSH contract that
// answers reference the questions the model asked.
func renderAnswers(answers map[string]any) string {
	if len(answers) == 0 {
		return "The user dismissed the questions without providing answers."
	}
	ids := make([]string, 0, len(answers))
	for id := range answers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var rendered strings.Builder
	rendered.WriteString("The user answered your questions:")
	for _, id := range ids {
		fmt.Fprintf(&rendered, "\n- %s: %s", id, formatAnswer(answers[id]))
	}
	return rendered.String()
}

func formatAnswer(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			parts = append(parts, fmt.Sprintf("%v", item))
		}
		return strings.Join(parts, ", ")
	case []string:
		return strings.Join(typed, ", ")
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", typed)
	}
}

// QuestionsFromPayload extracts the question round from an approval
// request payload. In-process requests carry the typed slice; requests
// that traveled through JSON (server, durable inbox) carry generic maps.
// Brokers use it to render the questions without depending on the tool's
// internals.
func QuestionsFromPayload(payload map[string]any) ([]Question, bool) {
	switch typed := payload["questions"].(type) {
	case []Question:
		return typed, len(typed) > 0
	case []any:
		questions := make([]Question, 0, len(typed))
		for _, item := range typed {
			fields, ok := item.(map[string]any)
			if !ok {
				return nil, false
			}
			question := Question{}
			question.ID, _ = fields["id"].(string)
			question.Question, _ = fields["question"].(string)
			question.Header, _ = fields["header"].(string)
			question.MultiSelect, _ = fields["multi_select"].(bool)
			if rawOptions, ok := fields["options"].([]any); ok {
				for _, rawOption := range rawOptions {
					optionFields, ok := rawOption.(map[string]any)
					if !ok {
						continue
					}
					option := Option{}
					option.Label, _ = optionFields["label"].(string)
					option.Description, _ = optionFields["description"].(string)
					question.Options = append(question.Options, option)
				}
			}
			questions = append(questions, question)
		}
		return questions, len(questions) > 0
	default:
		return nil, false
	}
}

// summarizeQuestions renders a short human-readable digest for the
// approval request description, bounded so long question sets cannot
// inflate the request.
func summarizeQuestions(questions []Question) string {
	var summary strings.Builder
	for i, question := range questions {
		if i > 0 {
			summary.WriteString("\n")
		}
		fmt.Fprintf(&summary, "%d. %s", i+1, strings.TrimSpace(question.Question))
		if len(question.Options) > 0 {
			labels := make([]string, 0, len(question.Options))
			for _, option := range question.Options {
				labels = append(labels, strings.TrimSpace(option.Label))
			}
			fmt.Fprintf(&summary, " (options: %s)", strings.Join(labels, " | "))
		}
	}
	if summary.Len() > 1000 {
		return summary.String()[:1000] + "..."
	}
	return summary.String()
}
