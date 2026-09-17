package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/policy"
	"github.com/feiyu912/zenforge/tool"
	"github.com/feiyu912/zenforge/tools"
	workspacepkg "github.com/feiyu912/zenforge/workspace"
)

type Config struct {
	Workspace              workspacepkg.Workspace
	Snapshots              *SnapshotStore
	RequireReadBeforeWrite bool
	Policy                 policy.FilePolicy
	// Search limits follow the DSH tool-fs-search defaults when zero:
	// glob keeps 100 paths inline, grep keeps 250 matches with
	// 2000-byte line previews, and over-cap results carry footers (plus
	// the complete glob list when SearchSpill is configured).
	GlobMaxResults   int
	GlobMaxVisited   int
	GrepMaxMatches   int
	GrepMaxLineBytes int
	SearchSpill      SearchSpill
	// TurnDiffs optionally receives per-mutation original/updated
	// content from the Write and Edit tools so the agent can render
	// per-turn unified diffs (codex TurnDiff parity).
	TurnDiffs *TurnDiffStore
}

// Edit failures that the model can recover from by adjusting its
// arguments: a missing match or an ambiguous (non-unique) match.
var (
	ErrEditNotFound  = errors.New("edit target not found")
	ErrEditAmbiguous = errors.New("edit target is ambiguous")
)

func Tools(config Config) ([]tool.Tool, error) {
	read, err := Read(config)
	if err != nil {
		return nil, err
	}
	list, err := List(config)
	if err != nil {
		return nil, err
	}
	glob, err := Glob(config)
	if err != nil {
		return nil, err
	}
	grep, err := Grep(config)
	if err != nil {
		return nil, err
	}
	write, err := Write(config)
	if err != nil {
		return nil, err
	}
	edit, err := Edit(config)
	if err != nil {
		return nil, err
	}
	return []tool.Tool{read, list, glob, grep, write, edit}, nil
}

func Read(config Config) (tool.Tool, error) {
	if config.Workspace == nil {
		return nil, fmt.Errorf("%w: workspace is nil", tool.ErrInvalidTool)
	}
	base, err := tools.New("workspace_read", "Read a file in the configured workspace.", func(ctx context.Context, in readInput, call tool.Context) (readOutput, error) {
		data, err := config.Workspace.Read(ctx, in.Path)
		if err != nil {
			// A failed read observes the path as absent, which is what
			// authorizes creating it later (see Write).
			if errors.Is(err, workspacepkg.ErrPathNotFound) {
				config.Snapshots.RecordAbsentForRun(call.RunID, normalizeSnapshotPath(in.Path))
			}
			return readOutput{}, err
		}
		offset := in.Offset
		if offset < 0 {
			offset = 0
		}
		if offset > len(data) {
			offset = len(data)
		}
		limit := in.Limit
		remaining := len(data) - offset
		if limit <= 0 || limit > remaining {
			limit = remaining
		}
		end := offset + limit
		info, err := config.Workspace.Stat(ctx, in.Path)
		if err != nil {
			return readOutput{}, err
		}
		sum := sha256.Sum256(data)
		info.SHA256 = hex.EncodeToString(sum[:])
		config.Snapshots.RecordForRun(call.RunID, info)
		return readOutput{
			Path:      info.Path,
			Content:   string(data[offset:end]),
			Offset:    offset,
			Bytes:     end - offset,
			TotalSize: len(data),
			Truncated: end < len(data),
			Info:      info,
		}, nil
	})
	if err != nil {
		return nil, err
	}
	return withFilePolicy(base, config.Policy, policy.FileRead), nil
}

func List(config Config) (tool.Tool, error) {
	if config.Workspace == nil {
		return nil, fmt.Errorf("%w: workspace is nil", tool.ErrInvalidTool)
	}
	base, err := tools.New("workspace_list", "List files in the configured workspace.", func(ctx context.Context, in listInput) (listOutput, error) {
		entries, err := config.Workspace.List(ctx, in.Path)
		if err != nil {
			return listOutput{}, err
		}
		return listOutput{Path: in.Path, Entries: entries}, nil
	})
	if err != nil {
		return nil, err
	}
	return withFilePolicy(base, config.Policy, policy.FileList), nil
}

func Grep(config Config) (tool.Tool, error) {
	if config.Workspace == nil {
		return nil, fmt.Errorf("%w: workspace is nil", tool.ErrInvalidTool)
	}
	base, err := tools.New("workspace_grep", "Search text files in the configured workspace.", func(ctx context.Context, in grepInput) (grepOutput, error) {
		capMatches := config.GrepMaxMatches
		if capMatches <= 0 {
			capMatches = DefaultGrepMaxMatches
		}
		effective := capMatches
		if in.MaxMatches > 0 && in.MaxMatches < effective {
			effective = in.MaxMatches
		}
		// Ask for one extra match to learn whether the cap cut anything.
		matches, err := config.Workspace.Grep(ctx, workspacepkg.GrepQuery{
			Pattern:    in.Pattern,
			Path:       in.Path,
			MaxMatches: effective + 1,
		})
		if err != nil {
			return grepOutput{}, err
		}
		out := grepOutput{Matches: matches}
		if len(matches) > effective {
			out.Matches = matches[:effective]
			out.Capped = true
			out.Footer = fmt.Sprintf("Results limited to %d matches; more exist. Narrow the pattern or path.", effective)
		}
		lineCap := config.GrepMaxLineBytes
		if lineCap <= 0 {
			lineCap = DefaultGrepMaxLineBytes
		}
		for i := range out.Matches {
			out.Matches[i].Text = truncatePreview(out.Matches[i].Text, lineCap)
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	return withFilePolicy(base, config.Policy, policy.FileGrep), nil
}

func Write(config Config) (tool.Tool, error) {
	if config.Workspace == nil {
		return nil, fmt.Errorf("%w: workspace is nil", tool.ErrInvalidTool)
	}
	base, err := tools.New("workspace_write", "Write a file in the configured workspace.", func(ctx context.Context, in writeInput, call tool.Context) (writeOutput, error) {
		if in.Description == "" {
			return writeOutput{}, fmt.Errorf("%w: description is required", tool.ErrInvalidArguments)
		}
		if config.RequireReadBeforeWrite {
			if config.Snapshots == nil {
				return writeOutput{}, ErrSnapshotRequired
			}
			info, statErr := config.Workspace.Stat(ctx, in.Path)
			switch {
			case errors.Is(statErr, workspacepkg.ErrPathNotFound):
				// Creating a file requires the run to have observed
				// the path as absent first, so a write can never
				// blindly create or clobber an unseen path.
				if !config.Snapshots.AbsentObservedForRun(call.RunID, normalizeSnapshotPath(in.Path)) {
					return writeOutput{}, fmt.Errorf("%w: %s does not exist; read it first so the run observes its absence before creating it", ErrSnapshotRequired, in.Path)
				}
			case statErr != nil:
				return writeOutput{}, statErr
			default:
				if err := config.Snapshots.CheckForRun(call.RunID, info); err != nil {
					return writeOutput{}, err
				}
			}
		}
		var turnOriginal string
		turnExisted := false
		if config.TurnDiffs != nil {
			// Best-effort capture of the pre-write content for the
			// turn-diff tracker; a missing or unreadable file simply
			// reads as "created".
			if data, readErr := config.Workspace.Read(ctx, in.Path); readErr == nil {
				turnOriginal = string(data)
				turnExisted = true
			}
		}
		if err := config.Workspace.Write(ctx, in.Path, []byte(in.Content)); err != nil {
			return writeOutput{}, err
		}
		config.TurnDiffs.RecordChange(call.RunID, in.Path, turnOriginal, turnExisted, in.Content)
		info, err := config.Workspace.Stat(ctx, in.Path)
		if err != nil {
			return writeOutput{}, err
		}
		return writeOutput{Path: info.Path, Bytes: len(in.Content), Info: info}, nil
	})
	if err != nil {
		return nil, err
	}
	return withFilePolicy(base, config.Policy, policy.FileWrite), nil
}

// Edit builds the exact-match text replacement tool. The contract follows
// the reference harnesses: oldString must match the file content literally,
// a single match is required unless replaceAll is set, and (under
// RequireReadBeforeWrite) the file must have been read through
// workspace_read at its current version — the snapshot check is a
// compare-and-swap guard against external modification.
func Edit(config Config) (tool.Tool, error) {
	if config.Workspace == nil {
		return nil, fmt.Errorf("%w: workspace is nil", tool.ErrInvalidTool)
	}
	base, err := tools.New("workspace_edit", "Edit a file in the configured workspace by replacing exact text matches.", func(ctx context.Context, in editInput, call tool.Context) (editOutput, error) {
		if in.Description == "" {
			return editOutput{}, fmt.Errorf("%w: description is required", tool.ErrInvalidArguments)
		}
		if in.OldString == "" {
			return editOutput{}, fmt.Errorf("%w: oldString must be a non-empty string", tool.ErrInvalidArguments)
		}
		if in.OldString == in.NewString {
			return editOutput{}, fmt.Errorf("%w: oldString and newString must differ", tool.ErrInvalidArguments)
		}
		info, err := config.Workspace.Stat(ctx, in.Path)
		if err != nil {
			return editOutput{}, err
		}
		if config.RequireReadBeforeWrite {
			if config.Snapshots == nil {
				return editOutput{}, ErrSnapshotRequired
			}
			if err := config.Snapshots.CheckForRun(call.RunID, info); err != nil {
				return editOutput{}, err
			}
		}
		data, err := config.Workspace.Read(ctx, in.Path)
		if err != nil {
			return editOutput{}, err
		}
		content := string(data)
		matches := strings.Count(content, in.OldString)
		if matches == 0 {
			return editOutput{}, fmt.Errorf("%w: oldString was not found in %q", ErrEditNotFound, info.Path)
		}
		if matches > 1 && !in.ReplaceAll {
			return editOutput{}, fmt.Errorf("%w: oldString matched %d times in %q; provide a more specific oldString or set replaceAll to true", ErrEditAmbiguous, matches, info.Path)
		}
		replacements := -1
		if !in.ReplaceAll {
			replacements = 1
		}
		updated := strings.Replace(content, in.OldString, in.NewString, replacements)
		if err := config.Workspace.Write(ctx, in.Path, []byte(updated)); err != nil {
			return editOutput{}, err
		}
		config.TurnDiffs.RecordChange(call.RunID, in.Path, content, true, updated)
		info, err = config.Workspace.Stat(ctx, in.Path)
		if err != nil {
			return editOutput{}, err
		}
		message := fmt.Sprintf("The file %s has been updated successfully.", info.Path)
		if matches > 1 {
			message = fmt.Sprintf("All %d occurrences in %s were successfully replaced.", matches, info.Path)
		}
		return editOutput{
			Path:         info.Path,
			Replacements: matches,
			Bytes:        len(updated),
			Message:      message,
			Info:         info,
		}, nil
	})
	if err != nil {
		return nil, err
	}
	return withFilePolicy(base, config.Policy, policy.FileWrite), nil
}

type filePolicyTool struct {
	base      tool.Tool
	policy    policy.FilePolicy
	operation policy.FileOperation
}

type filePolicyInput struct {
	Path        string `json:"path"`
	Content     string `json:"content"`
	OldString   string `json:"oldString"`
	NewString   string `json:"newString"`
	Description string `json:"description"`
}

func withFilePolicy(base tool.Tool, filePolicy policy.FilePolicy, operation policy.FileOperation) tool.Tool {
	return filePolicyTool{base: base, policy: filePolicy, operation: operation}
}

func (t filePolicyTool) Name() string {
	return t.base.Name()
}

func (t filePolicyTool) Description() string {
	return t.base.Description()
}

func (t filePolicyTool) Schema() map[string]any {
	return t.base.Schema()
}

func (t filePolicyTool) Call(ctx context.Context, raw json.RawMessage, call tool.Context) (tool.Result, error) {
	in, err := decodeFilePolicyInput(raw)
	if err != nil {
		return tool.Result{Error: tool.ErrInvalidArguments.Error(), ExitCode: 1}, err
	}
	if t.operation == policy.FileWrite && strings.TrimSpace(in.Description) == "" {
		return tool.Result{Error: tool.ErrInvalidArguments.Error(), ExitCode: 1}, fmt.Errorf("%w: description is required", tool.ErrInvalidArguments)
	}
	accessPlan := policy.PlanFileAccess(t.policy, t.operation, in.Path)
	fingerprint := accessPlan.Fingerprint
	ruleKey := accessPlan.RuleKey
	payload := map[string]any{
		"operation":   "workspace." + string(t.operation),
		"path":        accessPlan.Path,
		"rawPath":     accessPlan.RawPath,
		"fingerprint": fingerprint,
		"ruleKey":     ruleKey,
		"accessPlan":  accessPlan,
	}
	if t.operation == policy.FileWrite {
		writeContent := in.Content
		if writeContent == "" && in.NewString != "" {
			// Edits carry their payload in newString; the approval
			// fingerprint must cover the actual mutation.
			writeContent = in.NewString
		}
		writePlan := policy.PlanFileWrite(accessPlan, writeContent, in.Description)
		fingerprint = writePlan.Fingerprint
		ruleKey = writePlan.RuleKey
		payload["fingerprint"] = fingerprint
		payload["ruleKey"] = ruleKey
		payload["writePlan"] = writePlan
	}
	if accessPlan.Allowed {
		return t.base.Call(ctx, raw, call)
	}
	if accessPlan.RequiresApproval {
		if approval.MatchesApprovedMetadata(call.Metadata, fingerprint, ruleKey) {
			return t.base.Call(ctx, raw, call)
		}
		req := approval.Request{
			ID:          approval.NewRequestID(call.RunID, call.ToolCallID, t.Name()),
			RunID:       call.RunID,
			ToolCallID:  call.ToolCallID,
			ToolName:    t.Name(),
			Operation:   "workspace." + string(t.operation),
			Title:       "Approve workspace " + string(t.operation),
			Description: in.Description,
			Risk:        fileRisk(t.operation),
			Options:     approval.DefaultOptions(),
			Payload:     payload,
			CreatedAt:   time.Now().UTC(),
		}
		return approval.RequiredResult(req), approval.ErrRequired
	}
	return tool.Result{
		Error:      policy.ErrFileAccessDenied.Error(),
		ExitCode:   1,
		Structured: payload,
	}, policy.ErrFileAccessDenied
}

func decodeFilePolicyInput(raw json.RawMessage) (filePolicyInput, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var in filePolicyInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return filePolicyInput{}, fmt.Errorf("%w: %v", tool.ErrInvalidArguments, err)
	}
	return in, nil
}

func fileRisk(operation policy.FileOperation) approval.RiskLevel {
	if operation == policy.FileWrite {
		return approval.RiskHigh
	}
	return approval.RiskMedium
}

type readInput struct {
	Path   string `json:"path" jsonschema:"required,description=Workspace-relative file path"`
	Offset int    `json:"offset,omitempty" jsonschema:"description=Byte offset to start reading"`
	Limit  int    `json:"limit,omitempty" jsonschema:"description=Maximum bytes to return"`
}

type readOutput struct {
	Path      string                `json:"path"`
	Content   string                `json:"content"`
	Offset    int                   `json:"offset"`
	Bytes     int                   `json:"bytes"`
	TotalSize int                   `json:"totalSize"`
	Truncated bool                  `json:"truncated"`
	Info      workspacepkg.FileInfo `json:"info"`
}

type listInput struct {
	Path string `json:"path" jsonschema:"required,description=Workspace-relative directory path"`
}

type listOutput struct {
	Path    string                  `json:"path"`
	Entries []workspacepkg.FileInfo `json:"entries"`
}

type grepInput struct {
	Pattern    string `json:"pattern" jsonschema:"required,description=Regular expression pattern"`
	Path       string `json:"path" jsonschema:"required,description=Workspace-relative search path"`
	MaxMatches int    `json:"maxMatches,omitempty" jsonschema:"description=Maximum matches to return"`
}

type grepOutput struct {
	Matches []workspacepkg.Match `json:"matches"`
	Capped  bool                 `json:"capped,omitempty"`
	Footer  string               `json:"footer,omitempty"`
}

type writeInput struct {
	Path        string `json:"path" jsonschema:"required,description=Workspace-relative file path"`
	Content     string `json:"content" jsonschema:"required,description=File content"`
	Description string `json:"description" jsonschema:"required,description=Why this write is needed"`
}

type writeOutput struct {
	Path  string                `json:"path"`
	Bytes int                   `json:"bytes"`
	Info  workspacepkg.FileInfo `json:"info"`
}

type editInput struct {
	Path        string `json:"path" jsonschema:"required,description=Workspace-relative file path"`
	OldString   string `json:"oldString" jsonschema:"required,description=Literal text to replace; must match the file exactly"`
	NewString   string `json:"newString" jsonschema:"required,description=Replacement text; an empty string deletes the match"`
	ReplaceAll  bool   `json:"replaceAll,omitempty" jsonschema:"description=Replace every occurrence; when false oldString must match exactly once"`
	Description string `json:"description" jsonschema:"required,description=Why this edit is needed"`
}

type editOutput struct {
	Path         string                `json:"path"`
	Replacements int                   `json:"replacements"`
	Bytes        int                   `json:"bytes"`
	Message      string                `json:"message"`
	Info         workspacepkg.FileInfo `json:"info"`
}

func ResultContent(result tool.Result) string {
	if result.Output != "" {
		return result.Output
	}
	data, err := json.Marshal(result.Structured)
	if err != nil {
		return ""
	}
	return string(data)
}
