# Console Coverage Ledger

The browser console served by `zenforge serve` is the DSH console's own client
interface, vendored unchanged under `webui/dsh/`. It is a *client*: every
control on the page is backed by a remote method the host is asked to answer.
This ledger is the honest state of that surface — which methods this host
answers, which it refuses by name, and which it does not serve at all.

It is documentation of the [console adapter tier](architecture.md), not of the
framework: the framework's own API is the deep API and the harness core, and it
depends on none of this. See
[ADR 0099](adr/0099-the-framework-core-and-the-console-adapter-are-separate-layers.md).

## How to read it

| State | Meaning |
| --- | --- |
| `served` | The host answers the method. With valid arguments it does the work. |
| `stream` | The method is a logical stream on the WebSocket mux, not a unary call. |
| `refused` | The method is routed, but this host has no such capability and says so by name. The refusal text is given below. |
| `unserved` | No route: the client receives `404`. The panel that needs it stays empty or disabled. |

A `refused` method is deliberate: the host names what it cannot do instead of
returning an empty answer that would read as success (ADR 0082). `unserved`
methods are the remaining work, in priority order at the end of this page.

## Summary

This host answers **30** of the client's **109** methods, mounts
**2** of them as logical streams, refuses **15** by name, and
does not serve the remaining **62**.

| State | Count |
| --- | --- |
| served | 30 |
| stream | 2 |
| refused | 15 |
| unserved | 62 |
| **client methods total** | **109** |

The WebSocket mux also mounts `$events`, which is not part of the client's
method list.

## Served methods

| Method | State | What it answers |
| --- | --- | --- |
| `agentPresets/list` | served | The execution presets this host was built with. |
| `agentPresets/read` | served | One preset's definition. |
| `commands/execute` | served | Run one slash command. |
| `commands/list` | served | The workspace's slash commands. |
| `credentials/describe` | served | Whether a credential reference is set — never its value. |
| `credentials/set` | served | Store the one credential this host holds, in memory. |
| `credentials/unset` | served | Clear the stored credential. |
| `llm/discoverModels` | served | Interrogate a draft endpoint, or answer from a declared profile. |
| `llm/listConfigurableProviders` | served | The provider directory: declared profiles and configurable families. |
| `llm/listProviders` | served | The registered provider routes. |
| `permissionPresets/catalog` | served | The permission presets the host's own settings offer. |
| `pluginInventory/list` | served | The console bundles and plugins this host ships. |
| `session/cancel` | served | Stop a run. |
| `session/create` | served | Create a session. |
| `session/list` | served | List sessions. |
| `session/modelCatalog` | served | The models the page may offer, grouped per provider. |
| `session/page` | served | A page of a session's events. |
| `session/prompt` | served | Send a turn into a session. |
| `session/rename` | served | Rename a session. |
| `session/selectModel` | served | Choose the provider and model a session runs on. |
| `settings/canOpenAgentPresetDirectory` | served | Whether a native editor can be opened here (it cannot). |
| `settings/describe` | served | The settings namespaces, their schema and their values. |
| `settings/mutate` | served | Apply settings operations. |
| `settings/replace` | served | Replace a settings value. |
| `settings/update` | served | Update a settings value. |
| `workspaceFiles/list` | served | List a directory in a session's workspace. |
| `workspaceFiles/read` | served | Read a workspace file. |
| `workspaceFiles/readAll` | served | Read a file with its full context. |
| `workspaceFiles/readBytes` | served | Read a byte range of a file. |
| `workspaceFiles/stat` | served | Stat a workspace path. |
| `session/control` | stream | The control stream: job and model-selection projections. |
| `session/follow` | stream | The follow stream: a session's durable event log. |

## Refused by name

| Method | State | Why this host refuses |
| --- | --- | --- |
| `agentPresets/copy` | refused | this host's execution presets are built in; it has no preset directory to author |
| `agentPresets/deletePreset` | refused | this host's execution presets are built in; it has no preset directory to author |
| `agentPresets/select` | refused | this host fixes its execution preset at startup (--mode), so a session cannot select one; start the host with the mode the session needs |
| `pluginManager/cancelInstall` | refused | this host ships a fixed set of console bundles and has no loader, so it cannot install, enable, disable or remove a plugin |
| `pluginManager/inspect` | refused | this host ships a fixed set of console bundles and has no loader, so it cannot install, enable, disable or remove a plugin |
| `pluginManager/installBundle` | refused | this host ships a fixed set of console bundles and has no loader, so it cannot install, enable, disable or remove a plugin |
| `pluginManager/listBundles` | refused | this host ships a fixed set of console bundles and has no loader, so it cannot install, enable, disable or remove a plugin |
| `pluginManager/listPlugins` | refused | this host ships a fixed set of console bundles and has no loader, so it cannot install, enable, disable or remove a plugin |
| `pluginManager/removeBundle` | refused | this host ships a fixed set of console bundles and has no loader, so it cannot install, enable, disable or remove a plugin |
| `pluginManager/setBundleEnabled` | refused | this host ships a fixed set of console bundles and has no loader, so it cannot install, enable, disable or remove a plugin |
| `pluginManager/setPluginEnabled` | refused | this host ships a fixed set of console bundles and has no loader, so it cannot install, enable, disable or remove a plugin |
| `settings/openAgentPresetDirectory` | refused | this host has no native editor to open a settings document or a preset directory in; configure the host with --base-url, --model, --api-key or the settings panel instead |
| `settings/openSettingsDocument` | refused | this host has no native editor to open a settings document or a preset directory in; configure the host with --base-url, --model, --api-key or the settings panel instead |
| `workspaceFiles/changes` | refused | this host does not watch workspace files; reopen the file to see its current content |
| `workspaceFiles/readRelated` | refused | this host does not read a file relative to another; read the file by its own path |

## Not served

| Method | State | Why the panel stays empty |
| --- | --- | --- |
| `agentTeams/createTask` | unserved | no agent-team feature in this host |
| `agentTeams/updateTask` | unserved | no agent-team feature in this host |
| `agentTeams/view` | unserved | no agent-team feature in this host |
| `directoryPicker/createDirectory` | unserved | no browse backend is mounted — see Next up |
| `directoryPicker/list` | unserved | no browse backend is mounted — see Next up |
| `directoryPicker/pick` | unserved | no browse backend is mounted — see Next up |
| `dynamicCordisRunner/getClientCode` | unserved | no dynamic plugin runtime in this host |
| `dynamicCordisRunner/inventory` | unserved | no dynamic plugin runtime in this host |
| `dynamicCordisRunner/invoke` | unserved | no dynamic plugin runtime in this host |
| `dynamicCordisRunner/reportClientGuardFailure` | unserved | no dynamic plugin runtime in this host |
| `dynamicCordisRunner/reportRenderFailure` | unserved | no dynamic plugin runtime in this host |
| `dynamicCordisRunner/resolveInspectQuery` | unserved | no dynamic plugin runtime in this host |
| `dynamicCordisRunner/resolveRequestRun` | unserved | no dynamic plugin runtime in this host |
| `dynamicCordisRunner/runHostHalf` | unserved | no dynamic plugin runtime in this host |
| `dynamicCordisRunner/settleUserRun` | unserved | no dynamic plugin runtime in this host |
| `dynamicCordisRunner/stopFromPanel` | unserved | no dynamic plugin runtime in this host |
| `dynamicCordisRunner/syncInspectManifest` | unserved | no dynamic plugin runtime in this host |
| `dynamicCordisRunner/undefineFromPanel` | unserved | no dynamic plugin runtime in this host |
| `fileReferences/list` | unserved | no file-reference index |
| `fileUploads/upload` | unserved | the console cannot upload files to this host |
| `goals/clear` | unserved | the framework has a goal registry; it is not exposed here yet |
| `goals/complete` | unserved | the framework has a goal registry; it is not exposed here yet |
| `goals/create` | unserved | the framework has a goal registry; it is not exposed here yet |
| `goals/edit` | unserved | the framework has a goal registry; it is not exposed here yet |
| `goals/get` | unserved | the framework has a goal registry; it is not exposed here yet |
| `goals/pause` | unserved | the framework has a goal registry; it is not exposed here yet |
| `goals/resume` | unserved | the framework has a goal registry; it is not exposed here yet |
| `messageFeedback/delete` | unserved | no message-feedback store |
| `messageFeedback/list` | unserved | no message-feedback store |
| `messageFeedback/put` | unserved | no message-feedback store |
| `officeToPdf/generation` | unserved | no document conversion in this host |
| `officeToPdf/render` | unserved | no document conversion in this host |
| `session/attachment` | unserved | no attachment store |
| `session/canOpenWorkspacePath` | unserved | workspace opening is not wired |
| `session/fork` | unserved | no session fork |
| `session/openWorkspacePath` | unserved | workspace opening is not wired |
| `session/search` | unserved | no session search |
| `session/updateQueue` | unserved | no queue editing |
| `sessionFeedback/record` | unserved | no session-feedback store |
| `sessionReferenceResolver/candidates` | unserved | no reference resolver |
| `skills/list` | unserved | no skill listing exposed to the console |
| `subagents/interruptByParent` | unserved | subagent tools exist in the framework, not exposed here |
| `subagents/list` | unserved | subagent tools exist in the framework, not exposed here |
| `subagents/prompt` | unserved | subagent tools exist in the framework, not exposed here |
| `terminal/close` | unserved | no embedded terminal in this host |
| `terminal/create` | unserved | no embedded terminal in this host |
| `terminal/environment` | unserved | no embedded terminal in this host |
| `terminal/follow` | unserved | no embedded terminal in this host |
| `terminal/list` | unserved | no embedded terminal in this host |
| `terminal/rename` | unserved | no embedded terminal in this host |
| `terminal/resize` | unserved | no embedded terminal in this host |
| `terminal/retain` | unserved | no embedded terminal in this host |
| `terminal/shells` | unserved | no embedded terminal in this host |
| `terminal/write` | unserved | no embedded terminal in this host |
| `workspace/archiveSession` | unserved | no workspace organizer (archive, rename, order) |
| `workspace/create` | unserved | no workspace organizer (archive, rename, order) |
| `workspace/delete` | unserved | no workspace organizer (archive, rename, order) |
| `workspace/follow` | unserved | no workspace organizer (archive, rename, order) |
| `workspace/insertBefore` | unserved | no workspace organizer (archive, rename, order) |
| `workspace/insertSessionBefore` | unserved | no workspace organizer (archive, rename, order) |
| `workspace/rename` | unserved | no workspace organizer (archive, rename, order) |
| `workspace/unarchiveSession` | unserved | no workspace organizer (archive, rename, order) |

## Next up

The gaps in the order they block the page, from what the console asks first:

1. **`directoryPicker/list`, `directoryPicker/createDirectory`, `directoryPicker/pick`**
   — the workspace picker's browse backend. Without them the picker cannot list
   or select anything. `list` and `createDirectory` are host filesystem reads
   and writes that this host can serve; `pick` opens the operator's native
   dialog, which a headless server cannot do and should refuse by name.
2. **`session/openWorkspacePath`, `session/canOpenWorkspacePath`** — opening a
   workspace into a session, the other half of selection.
3. **`session/search`, `session/fork`, `session/attachment`, `session/updateQueue`**
   — session management the sidebar offers.
4. **`goals/*`** — the goal panel. The framework has a goal registry
   (`goals/`); this is wiring, not new capability.
5. **`skills/list`, `subagents/list`, `subagents/prompt`, `subagents/interruptByParent`**
   — framework features that are not exposed to the console yet.
6. **`terminal/*`** — an embedded terminal, which this host does not claim.
7. **`workspace/*`** — archiving, renaming, and ordering workspaces; the
   session organizer.
8. **`messageFeedback/*`, `sessionFeedback/*`, `fileReferences/list`,
   `fileUploads/upload`, `officeToPdf/*`, `agentTeams/*`, `sessionReferenceResolver/candidates`,
   `dynamicCordisRunner/*`** — page features with no host-side counterpart yet.

## Regenerating this ledger

The method list is the console client's own declaration, and the states are
measured against a running host rather than asserted:

```bash
# 1. Every remote method the vendored client declares, from its generated types.
grep -h "^    '[a-z]*/[a-zA-Z]*':" \
  /path/to/dsh/packages/*/lib/typert.remote-client.d.ts \
  /path/to/dsh/packages/*/*/lib/typert.remote-client.d.ts | sort -u

# 2. Ask a throwaway host each one, with empty arguments: HTTP 404 means
#    unserved; a `code: "unimplemented"` envelope means refused by name.
zenforge serve -addr 127.0.0.1:8791 --approve never
curl -s -o /dev/null -w '%{http_code}\n' -X POST \
  http://127.0.0.1:8791/api/session/modelCatalog \
  -H 'content-type: application/json' \
  -d '{"type":"client-request","rpcId":"p","method":"session/modelCatalog","payload":{"args":{}}}'
```

`internal/dshapi/console_coverage_test.go` checks every row against the host's
own routing table and fails if this page disagrees with it in either direction,
so a route added or removed in code cannot leave this ledger stale. The tier
itself — the dependency rule and the vocabulary that must stay out of the core —
is pinned by `docs/console_boundary_test.go`.