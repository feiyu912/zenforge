// Package present provides the DSH-style present tool: the model
// declares existing workspace files as final deliverables, and the
// runtime records them in the durable event log. Presenting validates
// that every path exists as a regular file inside the workspace; it
// never copies or preserves contents, it only publishes references.
package present

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/feiyu912/zenforge/tool"
	"github.com/feiyu912/zenforge/tools"
	workspacepkg "github.com/feiyu912/zenforge/workspace"
)

// Name is the tool name exposed to the model.
const Name = "present"

// DefaultMaxFiles matches the DSH per-call delivery limit.
const DefaultMaxFiles = 8

// Description mirrors the DSH tool description: presenting is required
// for received deliverables, and it never replaces mentioning the path.
const Description = "Declare existing files accessible through the workspace as final deliverables. When a file you create or update is an output the user asked to receive, you must call present after writing it and before your final response, including files created through shell commands or code execution. Mentioning its path in your reply does not replace this call. The files must already exist."

// Config configures the tool.
type Config struct {
	// Workspace resolves and validates presented paths. Required.
	Workspace workspacepkg.Workspace
	// MaxFiles caps one call; zero or negative selects DefaultMaxFiles.
	MaxFiles int
}

type input struct {
	Files []inputFile `json:"files" jsonschema:"required,description=Files to present as deliverables"`
}

type inputFile struct {
	Path        string `json:"path" jsonschema:"required,description=Path of an existing regular file; relative paths resolve against the workspace root"`
	Description string `json:"description,omitempty" jsonschema:"description=Brief description for the user"`
}

// File is one validated delivered file.
type File struct {
	Path        string `json:"path"`
	Description string `json:"description,omitempty"`
}

type output struct {
	Files   []File `json:"files"`
	Message string `json:"message"`
}

// New builds the present tool.
func New(config Config) (tool.Tool, error) {
	if config.Workspace == nil {
		return nil, fmt.Errorf("%w: workspace is nil", tool.ErrInvalidTool)
	}
	maxFiles := config.MaxFiles
	if maxFiles <= 0 {
		maxFiles = DefaultMaxFiles
	}
	return tools.New(Name, Description, func(ctx context.Context, in input) (output, error) {
		if len(in.Files) == 0 || len(in.Files) > maxFiles {
			return output{}, fmt.Errorf("%w: present accepts 1 to %d files", tool.ErrInvalidArguments, maxFiles)
		}
		files := make([]File, 0, len(in.Files))
		for _, file := range in.Files {
			if strings.TrimSpace(file.Path) == "" {
				return output{}, fmt.Errorf("%w: present requires a non-empty file path", tool.ErrInvalidArguments)
			}
			info, err := config.Workspace.Stat(ctx, file.Path)
			if errors.Is(err, workspacepkg.ErrPathNotFound) {
				return output{}, fmt.Errorf("%w: cannot present %s: file not found. Check the path, create the file if needed, and retry.", workspacepkg.ErrPathNotFound, file.Path)
			}
			if err != nil {
				return output{}, err
			}
			if info.IsDir {
				return output{}, fmt.Errorf("%w: cannot present %s: not a regular file", tool.ErrInvalidArguments, file.Path)
			}
			files = append(files, File{Path: info.Path, Description: file.Description})
		}
		lines := make([]string, 0, len(files))
		for _, file := range files {
			lines = append(lines, "Presented "+file.Path)
		}
		return output{Files: files, Message: strings.Join(lines, "\n")}, nil
	})
}
