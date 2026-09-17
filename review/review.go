// Package review adds a second, adversarial pass over a finished run: a
// reviewer model looks at what the run did and either approves it or asks
// for changes. It ports the reference's review/guardian idea in the shape
// this project can verify — one model call with a strict JSON verdict — and
// keeps the enforcement decision explicit, because a reviewer that can
// silently block every run is as dangerous as one that never speaks.
package review

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/feiyu912/zenforge/model"
)

// Decision is a review's verdict.
type Decision string

const (
	// DecisionApprove means the reviewer found nothing that must change.
	DecisionApprove Decision = "approve"
	// DecisionRequestChanges means the reviewer found at least one problem
	// worth another turn.
	DecisionRequestChanges Decision = "request_changes"
	// DecisionComment means the reviewer has remarks but nothing blocking.
	DecisionComment Decision = "comment"
)

// ParseDecision resolves a verdict name.
func ParseDecision(name string) (Decision, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case string(DecisionApprove), "approved", "ok", "lgtm":
		return DecisionApprove, nil
	case string(DecisionRequestChanges), "request-changes", "changes", "block":
		return DecisionRequestChanges, nil
	case string(DecisionComment), "comments", "note":
		return DecisionComment, nil
	default:
		return "", fmt.Errorf("unknown review decision %q (want %s, %s, or %s)", name, DecisionApprove, DecisionRequestChanges, DecisionComment)
	}
}

// Severity is how much a finding matters.
type Severity string

const (
	SeverityLow      Severity = "low"
	SeverityMedium   Severity = "medium"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"
)

// ParseSeverity resolves a severity name, defaulting to medium for an empty
// value so a terse reviewer is still usable.
func ParseSeverity(name string) (Severity, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "":
		return SeverityMedium, nil
	case string(SeverityLow):
		return SeverityLow, nil
	case string(SeverityMedium), "moderate":
		return SeverityMedium, nil
	case string(SeverityHigh), "major":
		return SeverityHigh, nil
	case string(SeverityCritical), "blocker":
		return SeverityCritical, nil
	default:
		return "", fmt.Errorf("unknown finding severity %q", name)
	}
}

// Blocks reports whether a severity is worth another turn on its own.
func (s Severity) Blocks() bool {
	return s == SeverityHigh || s == SeverityCritical
}

// Finding is one concrete problem.
type Finding struct {
	Severity Severity `json:"severity"`
	Path     string   `json:"path,omitempty"`
	Line     int      `json:"line,omitempty"`
	Issue    string   `json:"issue"`
	// Suggestion is what to do instead; it is what makes a finding
	// actionable rather than merely discouraging.
	Suggestion string `json:"suggestion,omitempty"`
}

// Verdict is a review's result.
type Verdict struct {
	Decision Decision  `json:"decision"`
	Summary  string    `json:"summary,omitempty"`
	Findings []Finding `json:"findings,omitempty"`
	// Model names the reviewer, and Usage records what the review cost.
	Model string      `json:"model,omitempty"`
	Usage model.Usage `json:"usage,omitempty"`
}

// Blocking reports the findings that justify another turn.
func (v Verdict) Blocking() []Finding {
	blocking := make([]Finding, 0, len(v.Findings))
	for _, finding := range v.Findings {
		if finding.Severity.Blocks() {
			blocking = append(blocking, finding)
		}
	}
	return blocking
}

// RequestsChanges reports whether the run should keep working. A
// request_changes verdict with no blocking finding still counts, because the
// reviewer asked explicitly; an approve verdict with findings does not,
// because those findings are remarks.
func (v Verdict) RequestsChanges() bool {
	if v.Decision != DecisionRequestChanges {
		return false
	}
	return true
}

// Request is what a reviewer sees.
type Request struct {
	RunID    string
	Task     string
	Output   string
	Files    []string
	Diff     string
	Commands []string
	Failures []string
}

// Reviewer produces a verdict for a finished run.
type Reviewer interface {
	// Name identifies the reviewer.
	Name() string
	// Review returns a verdict. An error means the review did not happen,
	// which the caller must treat as "no review" rather than "approved".
	Review(ctx context.Context, request Request) (Verdict, error)
}

// Defaults.
const (
	DefaultMaxTokens   = 2000
	DefaultMaxFindings = 20
	DefaultTimeout     = 2 * time.Minute
	DefaultSortByIssue = true
)

// ReviewPrompt asks for an adversarial but concrete review. It is adapted
// from the reference's review/guardian posture: the reviewer's job is to
// find what is wrong, not to praise, and every finding must say what to do
// instead.
const ReviewPrompt = `You review another agent's finished work, adversarially.

Your job is to find what is wrong, not to praise. Read the task, the final
answer, and the diff of every file the run changed. Look for correctness
bugs, unhandled failure paths, security problems, tests that do not test
what they claim, and changes that do not do what the task asked.

Rules:
- Report only problems you can point at. No style opinions, no speculation
  about code you were not shown, and no restating the diff.
- Every finding must be actionable: say what to change.
- If the work is sound, say so and return no findings. Inventing a
  complaint to look useful is worse than approving.
- Severity is critical (data loss, security, broken build), high (wrong
  behaviour in a normal path), medium (a real but narrow problem), or low
  (worth mentioning, not worth another turn).

Return JSON only, shaped exactly like this:
{"decision":"approve|request_changes|comment","summary":"...","findings":[{"severity":"high","path":"internal/x.go","line":42,"issue":"...","suggestion":"..."}]}

Use decision "request_changes" only when the run must keep working.`

// ModelReviewer reviews with one model call.
type ModelReviewer struct {
	// Model performs the review. Required.
	Model model.Model
	// Prompt overrides ReviewPrompt.
	Prompt string
	// Label names the reviewer; empty uses "model".
	Label string
	// MaxTokens bounds the call.
	MaxTokens int
	// MaxFindings caps the findings kept.
	MaxFindings int
}

// Name returns the reviewer's label.
func (r ModelReviewer) Name() string {
	if strings.TrimSpace(r.Label) != "" {
		return r.Label
	}
	return "model"
}

// Review runs the review call.
func (r ModelReviewer) Review(ctx context.Context, request Request) (Verdict, error) {
	if r.Model == nil {
		return Verdict{}, fmt.Errorf("review model is required")
	}
	prompt := r.Prompt
	if strings.TrimSpace(prompt) == "" {
		prompt = ReviewPrompt
	}
	maxTokens := r.MaxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}
	maxFindings := r.MaxFindings
	if maxFindings <= 0 {
		maxFindings = DefaultMaxFindings
	}
	response, err := r.Model.Generate(ctx, model.Request{
		Messages: []model.Message{
			{Role: "system", Content: prompt},
			{Role: "user", Content: FormatRequest(request)},
		},
		ToolChoice: model.ToolChoiceNone,
		Meta: map[string]any{
			"zenforge.review":    true,
			"zenforge.maxTokens": maxTokens,
		},
	})
	if err != nil {
		return Verdict{}, fmt.Errorf("review: %w", err)
	}
	verdict, err := ParseVerdict(response.Message.Content)
	if err != nil {
		return Verdict{}, err
	}
	if len(verdict.Findings) > maxFindings {
		verdict.Findings = verdict.Findings[:maxFindings]
	}
	verdict.Model = r.Name()
	verdict.Usage = response.Usage
	return verdict, nil
}

// wireVerdict is the reviewer's JSON answer.
type wireVerdict struct {
	Decision string `json:"decision"`
	Summary  string `json:"summary,omitempty"`
	Findings []struct {
		Severity   string `json:"severity,omitempty"`
		Path       string `json:"path,omitempty"`
		Line       int    `json:"line,omitempty"`
		Issue      string `json:"issue"`
		Suggestion string `json:"suggestion,omitempty"`
	} `json:"findings,omitempty"`
}

// ParseVerdict reads a reviewer's JSON. A malformed verdict is an error, not
// an approval: a reviewer that failed to answer must not be read as "looks
// good".
func ParseVerdict(output string) (Verdict, error) {
	text := strings.TrimSpace(output)
	if strings.HasPrefix(text, "```") {
		if index := strings.Index(text, "\n"); index >= 0 {
			text = text[index+1:]
		}
		text = strings.TrimSuffix(strings.TrimSpace(text), "```")
	}
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return Verdict{}, fmt.Errorf("review returned no JSON object: %s", clip(text, 200))
	}
	var wire wireVerdict
	decoder := json.NewDecoder(strings.NewReader(text[start : end+1]))
	if err := decoder.Decode(&wire); err != nil {
		return Verdict{}, fmt.Errorf("review returned invalid JSON: %w", err)
	}
	decision, err := ParseDecision(wire.Decision)
	if err != nil {
		return Verdict{}, err
	}
	verdict := Verdict{Decision: decision, Summary: singleLine(wire.Summary)}
	for _, item := range wire.Findings {
		issue := singleLine(item.Issue)
		if issue == "" {
			// A finding with no issue is not actionable, so it is dropped
			// rather than shown as an empty bullet.
			continue
		}
		severity, err := ParseSeverity(item.Severity)
		if err != nil {
			return Verdict{}, err
		}
		verdict.Findings = append(verdict.Findings, Finding{
			Severity:   severity,
			Path:       singleLine(item.Path),
			Line:       item.Line,
			Issue:      issue,
			Suggestion: singleLine(item.Suggestion),
		})
	}
	if verdict.Decision == DecisionRequestChanges && len(verdict.Findings) == 0 && verdict.Summary == "" {
		// "Keep working" with no reason is not actionable, so the reviewer
		// is asked to answer again rather than the agent being sent in
		// circles.
		return Verdict{}, fmt.Errorf("review requested changes without saying what to change")
	}
	SortFindings(verdict.Findings)
	return verdict, nil
}

// SortFindings orders findings worst first, then by path, so a rendered
// review leads with what matters and is stable for a given verdict.
func SortFindings(findings []Finding) {
	rank := func(severity Severity) int {
		switch severity {
		case SeverityCritical:
			return 0
		case SeverityHigh:
			return 1
		case SeverityMedium:
			return 2
		default:
			return 3
		}
	}
	sort.SliceStable(findings, func(i, j int) bool {
		if rank(findings[i].Severity) != rank(findings[j].Severity) {
			return rank(findings[i].Severity) < rank(findings[j].Severity)
		}
		if findings[i].Path != findings[j].Path {
			return findings[i].Path < findings[j].Path
		}
		return findings[i].Line < findings[j].Line
	})
}

// FormatRequest renders the review input with bounded sections, so a large
// diff cannot silently crowd out the task.
func FormatRequest(request Request) string {
	var builder strings.Builder
	builder.WriteString("## Task\n" + clip(request.Task, 2000) + "\n")
	builder.WriteString("\n## Final answer\n" + clip(request.Output, 3000) + "\n")
	if len(request.Files) > 0 {
		builder.WriteString("\n## Files changed\n")
		for _, file := range lastN(dedupe(request.Files), 60) {
			builder.WriteString("- " + singleLine(file) + "\n")
		}
	}
	if len(request.Commands) > 0 {
		builder.WriteString("\n## Commands run\n")
		for _, command := range lastN(request.Commands, 30) {
			builder.WriteString("- " + clip(singleLine(command), 300) + "\n")
		}
	}
	if len(request.Failures) > 0 {
		builder.WriteString("\n## Failures observed\n")
		for _, failure := range lastN(request.Failures, 15) {
			builder.WriteString("- " + clip(singleLine(failure), 300) + "\n")
		}
	}
	if strings.TrimSpace(request.Diff) != "" {
		builder.WriteString("\n## Diff\n" + clip(request.Diff, 20000) + "\n")
	}
	return builder.String()
}

// FormatFindings renders a verdict as an instruction for the agent. It is
// what an enforcing reviewer sends back as the next thing to fix.
func FormatFindings(verdict Verdict, budget int) string {
	if budget <= 0 {
		budget = 4000
	}
	var builder strings.Builder
	builder.WriteString("An independent review of your work found problems that must be fixed before you finish:\n")
	if verdict.Summary != "" {
		builder.WriteString("\n" + verdict.Summary + "\n")
	}
	for _, finding := range verdict.Findings {
		line := "\n- "
		if finding.Path != "" {
			line += finding.Path
			if finding.Line > 0 {
				line += fmt.Sprintf(":%d", finding.Line)
			}
			line += ": "
		}
		line += "[" + string(finding.Severity) + "] " + finding.Issue
		if finding.Suggestion != "" {
			line += " -> " + finding.Suggestion
		}
		if builder.Len()+len(line) > budget {
			builder.WriteString("\n- [more findings omitted: the review exceeded its size budget]")
			break
		}
		builder.WriteString(line)
	}
	return builder.String()
}

// SeverityCounts counts findings by severity, for logs and events.
func SeverityCounts(verdict Verdict) map[string]int {
	counts := map[string]int{}
	for _, finding := range verdict.Findings {
		counts[string(finding.Severity)]++
	}
	return counts
}

func singleLine(text string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(text, "\n", " ")), " ")
}

func clip(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit] + "\n[truncated]"
}

func lastN(values []string, n int) []string {
	if n <= 0 || len(values) <= n {
		return values
	}
	return values[len(values)-n:]
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
