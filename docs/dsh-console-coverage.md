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

This host answers **45** of the client's **109** methods, mounts
**4** of them as logical streams, refuses **16** by name, and
does not serve the remaining **44**.

| State | Count |
| --- | --- |
| served | 45 |
| stream | 4 |
| refused | 16 |
| unserved | 44 |
| **client methods total** | **109** |

The WebSocket mux also mounts `$events`, which is not part of the client's
method list: its opening frame is `ready`, and it delivers one `approval/request`
waterfall per pending approval, naming the **conversation** so the console can
attach the prompt to the session it has open (ADR 0115).

## Served methods

| Method | State | What it answers |
| --- | --- | --- |
| `agentPresets/list` | served | The execution presets this host was built with. The default is plan-execute, which creates a todo plan only when the request needs one (ADR 0107). |
| `agentPresets/read` | served | One preset's definition. |
| `commands/execute` | served | Run one slash command. |
| `commands/list` | served | The workspace's slash commands. |
| `credentials/describe` | served | Whether a credential reference is set — never its value. |
| `credentials/set` | served | Store the one credential this host holds, in its `0600` settings document. |
| `credentials/unset` | served | Clear the stored credential. |
| `directoryPicker/createDirectory` | served | Create one child directory under an existing parent. |
| `directoryPicker/list` | served | One directory level with its ancestry, for the in-app browser. |
| `goals/clear` | served | Remove the session's current goal. The answer is the ref that was removed, and the store drops the document rather than tombstoning it (ADR 0125). |
| `goals/complete` | served | Finish the current goal at one exact revision. |
| `goals/create` | served | Set a session's goal from its objective and an optional round cap. The id is the host's to mint, and the answer is the `{id, revision}` a following mutation compare-and-sets against (ADR 0125). |
| `goals/edit` | served | Change the objective and/or the round cap at one exact revision. |
| `goals/get` | served | The session's current goal, or no value at all when it has none — a null would reach the dock as a goal (ADR 0125). |
| `goals/pause` | served | Pause the current goal, keeping it and its round count. |
| `goals/resume` | served | Resume a paused goal at one exact revision. |
| `llm/discoverModels` | served | Interrogate a draft endpoint, or answer from a declared profile. |
| `llm/listConfigurableProviders` | served | The provider directory: declared profiles and configurable families. |
| `llm/listProviders` | served | The registered provider routes. |
| `permissionPresets/catalog` | served | The permission presets the host's own settings offer. |
| `pluginInventory/list` | served | The console bundles and plugins this host ships. |
| `session/canOpenWorkspacePath` | served | Whether this host can hand a workspace path to a native desktop: `false`, as the reference's own bare boolean, because this host serves the browser carrier and implements no desktop carrier (ADR 0126). |
| `session/cancel` | served | Stop the conversation's newest turn. The console names the session it has open (the first turn's id) while a multi-turn conversation runs `<session>~<k>`, so cancel resolves the chain ADR 0108 already builds and cancels the newest turn, accepting it idempotently and answering a conflict that names the turn it refused (ADR 0113). |
| `session/create` | served | Create a session. It exists before its first turn: its history is empty, not missing (ADR 0104). |
| `session/list` | served | List sessions. The list is the host's durable run registry, so it survives a restart and does not expire at the terminal retention; a record whose run never wrote an event is omitted because the console cannot open it (ADR 0109). A planning session is listed by the operator's own task; the plan-execute preset's appended instruction never reaches the title (ADR 0106). |
| `session/modelCatalog` | served | The models the page may offer, grouped per provider. |
| `session/page` | served | A page of a session's events, projected into the console's vocabulary. A created session with no turns answers an empty page (ADR 0104, ADR 0105). A session's turns share one sequence, so `Load earlier` reaches an earlier prompt (ADR 0108), and every message is identified by that sequence, so a second question renders as itself instead of matching the first question's node (ADR 0110). |
| `session/prompt` | served | Send a turn into a session. Under the plan-execute preset a question is answered in the plan stage and never reaches an execute or summary stage (ADR 0107). Every turn the host starts records the prompt's `requestId` as the run's `PromptID`, and the projected message publishes it as `source.rpcId`, which is what retires the console's local submission echo; a queued turn's text is projected from `request.steer`'s `message` (ADR 0111). |
| `session/rename` | served | Rename a session. |
| `session/selectModel` | served | Choose the provider and model a session runs on. The choice is restored on the next start (ADR 0103). |
| `settings/canOpenAgentPresetDirectory` | served | Whether a native editor can be opened here (it cannot). |
| `settings/describe` | served | The settings namespaces, their schema, their resolved values, the section the console itself wrote, and whether a document holds them (ADR 0102, ADR 0103). |
| `settings/mutate` | served | Apply settings operations. |
| `settings/replace` | served | Replace a settings value. |
| `settings/update` | served | Update a settings value. |
| `workspace/archiveSession` | served | Move a session into a workspace's archived list. |
| `workspace/create` | served | Register a directory as a workspace. |
| `workspace/delete` | served | Remove a registration; the host's own workspace is refused by name. |
| `workspace/rename` | served | Retitle a workspace. |
| `workspace/unarchiveSession` | served | Restore an archived session. |
| `workspaceFiles/list` | served | List a directory in a session's workspace. |
| `workspaceFiles/read` | served | Read a workspace file. |
| `workspaceFiles/readAll` | served | The whole file as base64 bytes, the arm the console's document preview decodes (ADR 0120). |
| `workspaceFiles/readBytes` | served | Read a byte range of a file. |
| `workspaceFiles/stat` | served | Stat a workspace path. |
| `session/control` | stream | The control stream: job and model-selection projections. |
| `session/follow` | stream | The follow stream: a session's durable event log, projected into the console's vocabulary. A draft's stream opens empty and waits for its first turn (ADR 0104, ADR 0105). A resumed stream cites a cursor ahead of the one the console already applied, so a second prompt does not break history loading (ADR 0108). A turn ending does not end the stream: it waits for the conversation's next turn and continues the same sequence, because the client treats a clean end after the snapshot as a carrier failure and reconnects over whatever `Load earlier` fetched (ADR 0114). The answer streams: the same durable deltas are minted into the console's dense `assistant-stream` frames (start/block-start/chunk/block-end/end, a per-frame revision, the settlement released by the end frame that names it), so prose renders as it arrives instead of at the step's settlement (ADR 0116). The window holds the console's events alone -- the host's bookkeeping and the deltas produce no records and consume no sequence numbers -- and the served sequence numbers records rather than durable events, so a conversation of several turns fits the window (ADR 0117). A reconnect in the middle of an answer is handed the attempt that is still streaming (`assistantStream.activeAttempt`, with the compact prefix of its chunks), and the live tail continues it instead of announcing a second start (ADR 0118); the snapshot cites the accumulator's real frame counter and publishes the conversation's `title` projection (ADR 0119). |
| `workspace/follow` | stream | The workspace stream: every registration and the archived set. |

## Refused by name

| Method | State | Why this host refuses |
| --- | --- | --- |
| `agentPresets/copy` | refused | this host's execution presets are built in; it has no preset directory to author |
| `agentPresets/deletePreset` | refused | this host's execution presets are built in; it has no preset directory to author |
| `agentPresets/select` | refused | this host fixes its execution preset at startup (--mode), so a session cannot select one; start the host with the mode the session needs |
| `directoryPicker/pick` | refused | this host has no operator display to open a native directory chooser on; directoryPicker/list and directoryPicker/createDirectory serve the console's in-app browser instead |
| `pluginManager/cancelInstall` | refused | this host ships a fixed set of console bundles and has no loader, so it cannot install, enable, disable or remove a plugin |
| `pluginManager/inspect` | refused | this host ships a fixed set of console bundles and has no loader, so it cannot install, enable, disable or remove a plugin |
| `pluginManager/installBundle` | refused | this host ships a fixed set of console bundles and has no loader, so it cannot install, enable, disable or remove a plugin |
| `pluginManager/listBundles` | refused | this host ships a fixed set of console bundles and has no loader, so it cannot install, enable, disable or remove a plugin |
| `pluginManager/listPlugins` | refused | this host ships a fixed set of console bundles and has no loader, so it cannot install, enable, disable or remove a plugin |
| `pluginManager/removeBundle` | refused | this host ships a fixed set of console bundles and has no loader, so it cannot install, enable, disable or remove a plugin |
| `pluginManager/setBundleEnabled` | refused | this host ships a fixed set of console bundles and has no loader, so it cannot install, enable, disable or remove a plugin |
| `pluginManager/setPluginEnabled` | refused | this host ships a fixed set of console bundles and has no loader, so it cannot install, enable, disable or remove a plugin |
| `session/openWorkspacePath` | refused | this host serves the console in a browser and has no desktop carrier to open a path on; workspaceFiles/list and workspaceFiles/read show a file inside the session instead |
| `settings/openAgentPresetDirectory` | refused | this host has no native editor to open a settings document or a preset directory in; configure the host with --base-url, --model, --api-key or the settings panel instead |
| `settings/openSettingsDocument` | refused | this host has no native editor to open a settings document or a preset directory in; configure the host with --base-url, --model, --api-key or the settings panel instead |
| `workspaceFiles/changes` | stream | The subscription a file resource opens before it stats anything: the `ready` frame unblocks the tab, and this host sends no `change` frames because it watches no files (ADR 0120). |
| `workspaceFiles/readRelated` | refused | this host does not read a file relative to another; read the file by its own path |

## Not served

| Method | State | Why the panel stays empty |
| --- | --- | --- |
| `agentTeams/createTask` | unserved | no agent-team feature in this host |
| `agentTeams/updateTask` | unserved | no agent-team feature in this host |
| `agentTeams/view` | unserved | no agent-team feature in this host |
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
| `messageFeedback/delete` | unserved | no message-feedback store |
| `messageFeedback/list` | unserved | no message-feedback store |
| `messageFeedback/put` | unserved | no message-feedback store |
| `officeToPdf/generation` | unserved | no document conversion in this host |
| `officeToPdf/render` | unserved | no document conversion in this host |
| `session/attachment` | unserved | no attachment store |
| `session/fork` | unserved | no session fork |
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
| `workspace/insertBefore` | unserved | no workspace organizer (archive, rename, order) |
| `workspace/insertSessionBefore` | unserved | no workspace organizer (archive, rename, order) |

## Next up

Audited **2026-09-22**. The list is a dated reading, not a description that stays true
by itself: it was re-derived from the host's routing table (`method` in
`internal/dshapi/handler.go`), the plugin roster (`internal/dshmount/roster.json`), and a
live `zenforge serve` whose boot graph and every advertised bundle were fetched. An item
that has shipped is removed rather than left to mislead the next window; the gaps below are
in the order they block the page, from what the console asks first.

1. **`session/search`, `session/fork`, `session/attachment`, `session/updateQueue`**
   — session management the sidebar offers.
2. **`skills/list`, `subagents/list`, `subagents/prompt`, `subagents/interruptByParent`**
   — framework features that are not exposed to the console yet.
3. **`terminal/*`** — an embedded terminal, which this host does not claim.
4. **`workspace/insertBefore`, `workspace/insertSessionBefore`** — the manual
   row order inside the workspace list. Registrations, titles, deletion and the
   archived set are served (ADR 0101); only the drag-to-reorder mutations are
   left.
5. **`messageFeedback/*`, `sessionFeedback/*`, `fileReferences/list`,
   `fileUploads/upload`, `officeToPdf/*`, `agentTeams/*`, `sessionReferenceResolver/candidates`,
   `dynamicCordisRunner/*`** — page features with no host-side counterpart yet.

The directory-picker family, which used to head this list, is no longer a gap: the browse
half is loaded from the roster and its bundle is served, so the workspace control's add
action has a dialog behind it. `directoryPicker/pick` stays refused by name — this host has
no operator display — and the native sibling is the roster's `blocked` entry, withheld with
the reason it actually needs (ADR 0100). Which plugins are withheld or omitted is the
roster's own answer, and it is the only place that claim is made:

```
$ curl -s http://127.0.0.1:8787/ | grep -o '__DSH_BOOT__.*'   # the served graph
53 entries, including @deepseek-ai/dsh-client-ui-directory-picker-browse
$ curl -s -o /dev/null -w '%{http_code}\n' \
    'http://127.0.0.1:8787/plugins/??@deepseek-ai/dsh-client-ui-directory-picker-browse/client.js'
200
$ python3 -c 'import json; r=json.load(open("internal/dshmount/roster.json")); print([b["dir"] for b in r["blocked"]])'
['client/ui-directory-picker-native', 'extensions/ui-cordis']
```

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