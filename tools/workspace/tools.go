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
}

func Tools(config Config) ([]tool.Tool, error) {
	read, err := Read(config)
	if err != nil {
		return nil, err
	}
	list, err := List(config)
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
	return []tool.Tool{read, list, grep, write}, nil
}

func Read(config Config) (tool.Tool, error) {
	if config.Workspace == nil {
		return nil, fmt.Errorf("%w: workspace is nil", tool.ErrInvalidTool)
	}
	base, err := tools.New("workspace_read", "Read a file in the configured workspace.", func(ctx context.Context, in readInput, call tool.Context) (readOutput, error) {
		data, err := config.Workspace.Read(ctx, in.Path)
		if err != nil {
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
		if limit <= 0 {
			limit = len(data)
		}
		end := offset + limit
		if end > len(data) {
			end = len(data)
		}
		info, _ := config.Workspace.Stat(ctx, in.Path)
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
		matches, err := config.Workspace.Grep(ctx, workspacepkg.GrepQuery{
			Pattern:    in.Pattern,
			Path:       in.Path,
			MaxMatches: in.MaxMatches,
		})
		if err != nil {
			return grepOutput{}, err
		}
		return grepOutput{Matches: matches}, nil
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
			info, err := config.Workspace.Stat(ctx, in.Path)
			if errors.Is(err, workspacepkg.ErrPathNotFound) {
				err = nil
			}
			if err != nil {
				return writeOutput{}, err
			}
			if info.Path != "" {
				if err := config.Snapshots.CheckForRun(call.RunID, info); err != nil {
					return writeOutput{}, err
				}
			}
		}
		if err := config.Workspace.Write(ctx, in.Path, []byte(in.Content)); err != nil {
			return writeOutput{}, err
		}
		info, err := config.Workspace.Stat(ctx, in.Path)
		if err != nil {
			return writeOutput{}, err
		}
		config.Snapshots.RecordForRun(call.RunID, info)
		return writeOutput{Path: info.Path, Bytes: len(in.Content), Info: info}, nil
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
		writePlan := policy.PlanFileWrite(accessPlan, in.Content, in.Description)
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
