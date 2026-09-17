// Package patch exposes the codex-style `apply_patch` tool: one
// freeform envelope that can add, update, move, and delete files in the
// configured workspace.
package patch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	engine "github.com/feiyu912/zenforge/applypatch"
	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/policy"
	"github.com/feiyu912/zenforge/tool"
	"github.com/feiyu912/zenforge/tools"
	workspacetools "github.com/feiyu912/zenforge/tools/workspace"
	workspacepkg "github.com/feiyu912/zenforge/workspace"
)

// Name is the tool name exposed to the model.
const Name = "apply_patch"

// Description documents the envelope format for the model.
const Description = `Apply a patch to files in the workspace. The patch is a plain-text envelope:

*** Begin Patch
*** Add File: path/to/new.txt
+new line
*** Update File: path/to/existing.txt
@@ optional context
-old line
+new line
*** Update File: path/to/old-name.txt
*** Move to: path/to/new-name.txt
*** Delete File: path/to/gone.txt
*** End Patch

Every line of a change must start with ' ' (context), '+' (added), or '-' (removed). A chunk begins with '@@' or '@@ <context>'; '*** End of File' anchors the previous chunk to the end of the file. Adding a file overwrites it when it already exists.`

// Config configures the tool.
type Config struct {
	// Workspace is the mutation target. Required.
	Workspace workspacepkg.Workspace
	// Snapshots enforces the read-before-write observation policy.
	Snapshots *workspacetools.SnapshotStore
	// RequireReadBeforeWrite requires an observed read (or observed
	// absence, for creations) before a patch mutates a path.
	RequireReadBeforeWrite bool
	// FilePolicy gates the patch behind the workspace file policy.
	FilePolicy policy.FilePolicy
	// TurnDiffs records per-path original/updated content for turn
	// diffs.
	TurnDiffs *workspacetools.TurnDiffStore
}

type input struct {
	Patch       string `json:"patch" jsonschema:"required,description=The patch envelope; see the tool description for the format"`
	Description string `json:"description" jsonschema:"required,description=Short summary of the change for the approval record"`
}

type output struct {
	Added    []string `json:"added,omitempty"`
	Modified []string `json:"modified,omitempty"`
	Deleted  []string `json:"deleted,omitempty"`
	Message  string   `json:"message"`
}

// New builds the apply_patch tool: a policy/approval shell around the
// pure patch applier, mirroring the workspace mutation tools.
func New(config Config) (tool.Tool, error) {
	if config.Workspace == nil {
		return nil, fmt.Errorf("%w: workspace is nil", tool.ErrInvalidTool)
	}
	base, err := tools.New(Name, Description, func(ctx context.Context, in input, call tool.Context) (output, error) {
		hunks, err := engine.Parse(in.Patch)
		if err != nil {
			return output{}, fmt.Errorf("%w: %s", tool.ErrInvalidArguments, err)
		}
		if err := checkObservationPolicy(ctx, config, call, hunks); err != nil {
			return output{}, err
		}
		recorder := &fsAdapter{
			ctx: ctx, config: config, runID: call.RunID,
			originals: map[string]string{}, updated: map[string]string{},
		}
		result, err := engine.Apply(hunks, recorder)
		if err != nil {
			return output{}, err
		}
		for path, original := range recorder.originals {
			config.TurnDiffs.RecordChange(call.RunID, path, original, true, recorder.updated[path])
		}
		for _, path := range result.Deleted {
			if config.Snapshots != nil {
				config.Snapshots.RecordAbsentForRun(call.RunID, path)
			}
		}
		return output{
			Added:    result.Added,
			Modified: result.Modified,
			Deleted:  result.Deleted,
			Message:  strings.TrimRight(result.Summary(), "\n"),
		}, nil
	})
	if err != nil {
		return nil, err
	}
	return policyTool{base: base, config: config}, nil
}

// policyTool gates a whole patch behind the workspace file policy. One
// approval authorizes the complete change set: the fingerprint covers
// the patch text, so approving a different patch cannot reuse it.
type policyTool struct {
	base   tool.Tool
	config Config
}

func (t policyTool) Name() string           { return t.base.Name() }
func (t policyTool) Description() string    { return t.base.Description() }
func (t policyTool) Schema() map[string]any { return t.base.Schema() }

func (t policyTool) Call(ctx context.Context, raw json.RawMessage, call tool.Context) (tool.Result, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.Result{Error: tool.ErrInvalidArguments.Error(), ExitCode: 1},
			fmt.Errorf("%w: %v", tool.ErrInvalidArguments, err)
	}
	if strings.TrimSpace(in.Description) == "" {
		return tool.Result{Error: tool.ErrInvalidArguments.Error(), ExitCode: 1},
			fmt.Errorf("%w: description is required", tool.ErrInvalidArguments)
	}
	hunks, err := engine.Parse(in.Patch)
	if err != nil {
		return tool.Result{Error: tool.ErrInvalidArguments.Error(), ExitCode: 1},
			fmt.Errorf("%w: %s", tool.ErrInvalidArguments, err)
	}
	if len(hunks) == 0 {
		return tool.Result{Error: tool.ErrInvalidArguments.Error(), ExitCode: 1},
			fmt.Errorf("%w: the patch does not change any file", tool.ErrInvalidArguments)
	}
	paths := affectedPaths(hunks)

	payload := map[string]any{
		"operation": "workspace.patch",
		"paths":     paths,
		"patch":     in.Patch,
	}
	writePlan := policy.PlanFileWrite(
		policy.PlanFileAccess(t.config.FilePolicy, policy.FileWrite, paths[0]), in.Patch, in.Description,
	)
	payload["fingerprint"] = writePlan.Fingerprint
	payload["ruleKey"] = writePlan.RuleKey
	payload["writePlan"] = writePlan

	requiresApproval := false
	for _, path := range paths {
		plan := policy.PlanFileAccess(t.config.FilePolicy, policy.FileWrite, path)
		if plan.Allowed {
			continue
		}
		if plan.RequiresApproval {
			requiresApproval = true
			continue
		}
		return tool.Result{
			Error:      policy.ErrFileAccessDenied.Error(),
			ExitCode:   1,
			Structured: payload,
		}, policy.ErrFileAccessDenied
	}
	if requiresApproval {
		if !approval.MatchesApprovedMetadata(call.Metadata, writePlan.Fingerprint, writePlan.RuleKey) {
			request := approval.Request{
				ID:          approval.NewRequestID(call.RunID, call.ToolCallID, Name),
				RunID:       call.RunID,
				ToolCallID:  call.ToolCallID,
				ToolName:    Name,
				Operation:   "workspace.patch",
				Title:       "Approve patch",
				Description: in.Description,
				Risk:        approval.RiskHigh,
				Options:     approval.DefaultOptions(),
				Payload:     payload,
				CreatedAt:   time.Now().UTC(),
			}
			return approval.RequiredResult(request), approval.ErrRequired
		}
	}
	return t.base.Call(ctx, raw, call)
}

// affectedPaths lists every path the patch touches, sorted for stable
// approval payloads.
func affectedPaths(hunks []engine.Hunk) []string {
	seen := map[string]struct{}{}
	var paths []string
	add := func(path string) {
		if path == "" {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	for _, hunk := range hunks {
		add(hunk.Path)
		add(hunk.MovePath)
	}
	sort.Strings(paths)
	return paths
}

// checkObservationPolicy mirrors the workspace write/edit rule: an
// update needs a same-run read of the current version, and a creation
// needs an observed absence.
func checkObservationPolicy(ctx context.Context, config Config, call tool.Context, hunks []engine.Hunk) error {
	if !config.RequireReadBeforeWrite {
		return nil
	}
	if config.Snapshots == nil {
		return workspacetools.ErrSnapshotRequired
	}
	for _, hunk := range hunks {
		if hunk.Kind == "delete" {
			// Deletion needs the file to exist; the applier reports a
			// missing path, and a read is not required to remove it.
			continue
		}
		info, err := config.Workspace.Stat(ctx, hunk.Path)
		switch {
		case err == nil:
			if err := config.Snapshots.CheckForRun(call.RunID, info); err != nil {
				return err
			}
		case errors.Is(err, workspacepkg.ErrPathNotFound):
			if hunk.Kind == "add" && !config.Snapshots.AbsentObservedForRun(call.RunID, hunk.Path) {
				return fmt.Errorf("%w: %s does not exist; read it first so the run observes its absence before creating it", workspacetools.ErrSnapshotRequired, hunk.Path)
			}
		default:
			return err
		}
	}
	return nil
}

// fsAdapter applies the patch through the workspace abstraction while
// remembering the original content for turn diffs.
type fsAdapter struct {
	ctx       context.Context
	config    Config
	runID     string
	originals map[string]string
	updated   map[string]string
}

func (a *fsAdapter) ReadFile(path string) ([]byte, error) {
	data, err := a.config.Workspace.Read(a.ctx, path)
	if err != nil {
		return nil, err
	}
	if _, seen := a.originals[path]; !seen {
		a.originals[path] = string(data)
	}
	return data, nil
}

func (a *fsAdapter) WriteFile(path string, data []byte) error {
	if err := a.config.Workspace.Write(a.ctx, path, data); err != nil {
		return err
	}
	if a.updated == nil {
		a.updated = map[string]string{}
	}
	a.updated[path] = string(data)
	if _, seen := a.originals[path]; !seen {
		a.originals[path] = ""
	}
	return nil
}

func (a *fsAdapter) Remove(path string) error {
	deleter, ok := a.config.Workspace.(workspacepkg.Deleter)
	if !ok {
		return fmt.Errorf("workspace %T does not support file deletion", a.config.Workspace)
	}
	if data, err := a.config.Workspace.Read(a.ctx, path); err == nil {
		if _, seen := a.originals[path]; !seen {
			a.originals[path] = string(data)
		}
	}
	if err := deleter.Delete(a.ctx, path); err != nil {
		return err
	}
	if a.updated == nil {
		a.updated = map[string]string{}
	}
	a.updated[path] = ""
	return nil
}

// MkdirAll is a no-op: the workspace creates parent directories for
// writes according to its own configuration.
func (a *fsAdapter) MkdirAll(string) error { return nil }

func (a *fsAdapter) IsDir(path string) (bool, error) {
	info, err := a.config.Workspace.Stat(a.ctx, path)
	if err != nil {
		return false, err
	}
	return info.IsDir, nil
}

var _ engine.FS = (*fsAdapter)(nil)
