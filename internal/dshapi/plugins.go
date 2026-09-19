package dshapi

import (
	"context"
	"encoding/json"
)

// Wire shapes for POST /api/pluginInventory/list. The console's Plugins settings
// tab renders it (client/ui-settings-plugin-inventory/src/client/index.ts:38) and
// the packages manager reads it before anything else: when
// `managementAvailable !== true` that panel reports itself unavailable and never
// calls pluginManager at all
// (client/ui-plugin-manager/src/client/manager-store.ts:461-470). Types mirror
// packages/host/plugin-inventory/lib/types/types.d.ts:4-32.

// PluginInventoryEntry is one module in the inventory. Upstream types.d.ts:4-9.
type PluginInventoryEntry struct {
	// EntryID identifies the host's entry for the module. Here it is the staged
	// directory the bundle is served from, because that is what the host keys
	// its published assets by.
	EntryID string `json:"entryId"`
	// ModuleName is the module specifier.
	ModuleName string `json:"moduleName"`
	// Enabled reports whether this host publishes the module.
	Enabled bool `json:"enabled"`
	// FiberPhase would report the client-side fiber's phase. It is always null:
	// the host serves static assets and does not observe the console's runtime,
	// so it has nothing to report and says so rather than claiming "active" for
	// a module it cannot see running.
	FiberPhase *string `json:"fiberPhase"`
}

// PluginInventorySnapshot answers pluginInventory/list. Upstream types.d.ts:27-32.
type PluginInventorySnapshot struct {
	// ManagementAvailable is false: this host ships a fixed set of console
	// bundles and has no loader that could enable, disable or install one at
	// runtime. The packages manager reads this and degrades on its own.
	ManagementAvailable bool `json:"managementAvailable"`
	// Entries is the published inventory, including the upstream siblings this
	// host deliberately withholds so the listing is complete rather than
	// convenient.
	Entries []PluginInventoryEntry `json:"entries"`
	// AgentPresets is omitted: it would describe presets as compositions of
	// plugin rows, and this host's presets are compiled in (ADR 0088), so there
	// are no rows to report. An empty array would claim presets exist with no
	// plugins rather than that this host does not compose them from plugins.
}

// PluginInventorySource reports the host's plugin inventory.
type PluginInventorySource func() PluginInventorySnapshot

// SetPluginInventory installs the inventory source.
func (h *Handler) SetPluginInventory(source PluginInventorySource) {
	h.pluginsMu.Lock()
	h.plugins = source
	h.pluginsMu.Unlock()
}

func (h *Handler) pluginInventorySource() PluginInventorySource {
	h.pluginsMu.RLock()
	defer h.pluginsMu.RUnlock()
	return h.plugins
}

// pluginInventoryList answers POST /api/pluginInventory/list.
func (h *Handler) pluginInventoryList(_ context.Context, args map[string]json.RawMessage) (any, *methodError) {
	if failure := rejectUnexpectedArguments("pluginInventory/list", args); failure != nil {
		return nil, failure
	}
	source := h.pluginInventorySource()
	if source == nil {
		return nil, fail(codeUnimplemented,
			"pluginInventory/list is not configured: the host has no plugin inventory; the console mount must install one with Handler.SetPluginInventory",
			map[string]any{"dependency": "PluginInventorySource"})
	}
	snapshot := source()
	if snapshot.Entries == nil {
		snapshot.Entries = []PluginInventoryEntry{}
	}
	return snapshot, nil
}

// pluginManagerUnsupported answers every write in the plugin-manager namespace.
//
// One refusal covers all of them because they share one reason: this host has no
// loader, so nothing an operator changes here could take effect. It carries
// managementAvailable false in the details, which is the same answer
// pluginInventory/list gives, so a caller that skipped the inventory still learns
// why the write cannot land.
func (h *Handler) pluginManagerUnsupported(_ context.Context, _ map[string]json.RawMessage) (any, *methodError) {
	return nil, fail(codeUnimplemented,
		"this host ships a fixed set of console bundles and has no loader, so it cannot install, enable, disable or remove a plugin",
		map[string]any{"capability": "host plugin management", "managementAvailable": false})
}
