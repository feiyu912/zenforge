package dshapi

import (
	"context"
	"encoding/json"
	"fmt"
)

// Wire shapes for the two preset namespaces the console asks for while it
// boots: POST /api/permissionPresets/catalog and
// POST /api/agentPresets/{list,read,...}. The console requests both on load (the
// composer's permission and preset selectors), so answering them is what turns
// two startup failures into two answered surfaces.
//
// Types mirror packages/interaction/permission-presets/lib/types/types.d.ts:1-12
// and packages/preset/agent-presets/lib/types/types.d.ts:1-36.

// PresetOption is one permission choice. Upstream types.ts:1-5.
type PresetOption struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// PermissionCatalog is the answer to permissionPresets/catalog. Upstream
// types.ts:6-8.
type PermissionCatalog struct {
	Options []PresetOption `json:"options"`
}

// AgentPresetRow is one preset in the roster. Upstream types.ts:4-11. Trust is
// 'system' or 'user' (preset.d.ts:7); this host builds its presets in, so every
// row is a system preset.
type AgentPresetRow struct {
	ID          string `json:"id"`
	Trust       string `json:"trust"`
	IsDefault   bool   `json:"isDefault"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	// Broken is set when a preset exists but cannot be read. This host's presets
	// are compiled in, so none can be broken and the field is never sent.
	Broken string `json:"broken,omitempty"`
}

// AgentPresetRoster is the answer to agentPresets/list. Upstream types.ts:12-16.
type AgentPresetRoster struct {
	Presets []AgentPresetRow `json:"presets"`
	// Authorable is false: authoring writes a preset directory on the host, and
	// this host has none.
	Authorable bool `json:"authorable"`
	// ModeSelectionEnabled is false: this host's execution preset is fixed at
	// startup, so the roster is a truthful list rather than a selector that
	// would silently do nothing.
	ModeSelectionEnabled bool `json:"modeSelectionEnabled"`
}

// AgentPresetDocument is the answer to agentPresets/read. Upstream types.ts:29-35.
type AgentPresetDocument struct {
	AgentPreset string `json:"agentPreset"`
	Trust       string `json:"trust"`
	Content     string `json:"content"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// ConsolePresetMode is one execution preset this host can run.
type ConsolePresetMode struct {
	// ID is the preset id the console passes back (react, oneshot, plan_execute).
	ID string
	// Name is the display name.
	Name string
	// Description says what the mode does, in this repository's own words.
	Description string
}

// ConsolePresets is the host's preset configuration as the console sees it: the
// sandbox and approval settings the host actually runs with, and the execution
// presets it can build. It is a snapshot, not a setter -- the console cannot
// change any of it here, and every surface below says so rather than offering a
// choice that would be dropped.
type ConsolePresets struct {
	// Sandbox is the host's --sandbox value (none, seatbelt, bwrap, docker).
	Sandbox string
	// Approval is the host's --approve value (never, prompt, always).
	Approval string
	// DefaultMode is the configured --mode value.
	DefaultMode string
	// Modes are the execution presets this host implements.
	Modes []ConsolePresetMode
}

// PresetSource reports the host's preset configuration.
type PresetSource func() ConsolePresets

// SetPresets installs the preset source the preset namespaces answer from.
func (h *Handler) SetPresets(source PresetSource) {
	h.presetsMu.Lock()
	h.presets = source
	h.presetsMu.Unlock()
}

func (h *Handler) presetSource() PresetSource {
	h.presetsMu.RLock()
	defer h.presetsMu.RUnlock()
	return h.presets
}

// permissionPresetOptions maps the host's sandbox and approval settings onto the
// names upstream uses, and names the raw combination when it matches none of
// them.
//
// Upstream's two named presets are combinations
// (permission-presets/src/index.ts:185-194): 'workspace-write' is a confined
// sandbox with approvals asked, and 'danger-full-access' is an unconfined
// sandbox that never asks. Anything else is 'custom' -- upstream's own reserved
// name for a derived state -- and its description states the settings it derived
// from, because an operator reading the panel should see why no named preset
// matched rather than a plausible-looking wrong one.
func permissionPresetOptions(presets ConsolePresets) []PresetOption {
	sandbox := presets.Sandbox
	if sandbox == "" {
		sandbox = "none"
	}
	approval := presets.Approval
	if approval == "" {
		approval = "prompt"
	}
	switch {
	case sandbox == "none" && approval == "never":
		return []PresetOption{{
			Value:       "danger-full-access",
			Name:        "danger-full-access",
			Description: "Full file access without approval prompts; this host was started with --sandbox none --approve never.",
		}}
	case sandbox != "none" && approval != "never":
		return []PresetOption{{
			Value:       "workspace-write",
			Name:        "workspace-write",
			Description: "Write inside the workspace and permitted temporary directories; wider retries require approval. This host was started with --sandbox " + sandbox + " --approve " + approval + ".",
		}}
	default:
		return []PresetOption{{
			Value: "custom",
			Name:  "custom",
			Description: "This host's own combination: --sandbox " + sandbox + " --approve " + approval +
				". It is fixed at startup, so the console cannot switch it.",
		}}
	}
}

// permissionPresetsCatalog answers POST /api/permissionPresets/catalog.
func (h *Handler) permissionPresetsCatalog(_ context.Context, args map[string]json.RawMessage) (any, *methodError) {
	if failure := rejectUnexpectedArguments(args); failure != nil {
		return nil, failure
	}
	source := h.presetSource()
	if source == nil {
		return nil, presetsDependencyMissing("permissionPresets/catalog")
	}
	return PermissionCatalog{Options: permissionPresetOptions(source())}, nil
}

// consolePresetRows builds the roster rows from the modes this host implements.
func consolePresetRows(presets ConsolePresets) []AgentPresetRow {
	rows := make([]AgentPresetRow, 0, len(presets.Modes))
	for _, mode := range presets.Modes {
		rows = append(rows, AgentPresetRow{
			ID:          mode.ID,
			Trust:       "system",
			IsDefault:   mode.ID == presets.DefaultMode,
			Name:        mode.Name,
			Description: mode.Description,
		})
	}
	return rows
}

// agentPresetsList answers POST /api/agentPresets/list.
func (h *Handler) agentPresetsList(_ context.Context, args map[string]json.RawMessage) (any, *methodError) {
	if failure := rejectUnexpectedArguments(args); failure != nil {
		return nil, failure
	}
	source := h.presetSource()
	if source == nil {
		return nil, presetsDependencyMissing("agentPresets/list")
	}
	return AgentPresetRoster{
		Presets: consolePresetRows(source()),
		// Authoring writes a preset directory and switching writes to a session's
		// log; this host does neither, and both fields say so rather than
		// leaving the console to discover it through a failing write.
		Authorable:           false,
		ModeSelectionEnabled: false,
	}, nil
}

// agentPresetsRead answers POST /api/agentPresets/read with the host's own
// description of the mode. A preset here is compiled in, so the content says what
// the mode does and states plainly that there is no file behind it.
func (h *Handler) agentPresetsRead(_ context.Context, args map[string]json.RawMessage) (any, *methodError) {
	agentPreset, _, failure := stringArg(args, "agentPreset")
	if failure != nil {
		return nil, failure
	}
	source := h.presetSource()
	if source == nil {
		return nil, presetsDependencyMissing("agentPresets/read")
	}
	presets := source()
	available := make([]string, 0, len(presets.Modes))
	for _, mode := range presets.Modes {
		available = append(available, mode.ID)
		if mode.ID == agentPreset {
			return AgentPresetDocument{
				AgentPreset: mode.ID,
				Trust:       "system",
				Name:        mode.Name,
				Description: mode.Description,
				Content: mode.Description + "\n\n" +
					"This host builds its execution presets into the binary, so there is no preset file to edit or copy.",
			}, nil
		}
	}
	// Upstream's own code and details for an id that is not in the roster
	// (agent-presets/lib/types/types.d.ts:20-25), so a console that handles the
	// upstream answer handles this one.
	return nil, fail("agent-preset/not-found",
		fmt.Sprintf("agent preset %q is not implemented by this host", agentPreset),
		map[string]any{"agentPreset": agentPreset, "available": available})
}

// agentPresetsReadOnly answers the two authoring writes. Upstream reports a
// read-only roster this way (agent-preset/read-only, types.d.ts:26-29); the
// detail carries the reason rather than a silently created directory.
func (h *Handler) agentPresetsReadOnly(_ context.Context, args map[string]json.RawMessage) (any, *methodError) {
	agentPreset, _, _ := stringArg(args, "agentPreset")
	return nil, fail("agent-preset/read-only",
		"this host's execution presets are built in; it has no preset directory to author",
		map[string]any{
			"agentPreset": agentPreset,
			"reason":      "the host was built without an agent preset directory",
		})
}

// agentPresetsSelectIsUnsupported answers the per-session selection: this host
// fixes its execution preset at startup, so accepting a selection would report a
// change that never reaches the run.
func (h *Handler) agentPresetsSelectIsUnsupported(_ context.Context, _ map[string]json.RawMessage) (any, *methodError) {
	return nil, fail(codeUnimplemented,
		"this host fixes its execution preset at startup (--mode), so a session cannot select one; start the host with the mode the session needs",
		map[string]any{"capability": "per-session agent preset selection"})
}

// presetsDependencyMissing is the honest answer when no source is installed.
func presetsDependencyMissing(method string) *methodError {
	return fail(codeUnimplemented,
		method+" is not configured: the host has no preset source; the serve command must install one with Handler.SetPresets",
		map[string]any{"dependency": "PresetSource"})
}
