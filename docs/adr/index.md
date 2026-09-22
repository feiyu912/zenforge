# Architecture Decision Records

This directory holds the [architecture decision records](https://adr.github.io/)
for ZenForge. Each ADR captures a significant design choice: the context, the
decision, and the consequences. They are immutable historical artifacts —
later ADRs supersede earlier ones, never edit.

!!! note "Reading order"
    The ADRs are numbered chronologically. Read in order to see how the
    framework's design solidified, or jump to a specific topic using the
    table of contents on the left. The optional DSH console adapter's own
    series is grouped below the main index, so the framework's decisions stay
    the main line of the record.

## Index

| Number | Topic |
| --- | --- |
| [0001](0001-event-log-and-checkpoint.md) | Event log and checkpoint stores |
| [0002](0002-public-event-contract.md) | Public event contract |
| [0003](0003-tool-runtime.md) | Tool runtime |
| [0004](0004-approval-broker.md) | Approval broker |
| [0005](0005-sub-agent-orchestration.md) | Sub-agent orchestration |
| [0006](0006-package-layout.md) | Package layout |
| [0007](0007-run-state-schema.md) | Run state schema |
| [0008](0008-tool-result-contract.md) | Tool result contract |
| [0009](0009-workspace-policy-separation.md) | Workspace policy separation |
| [0010](0010-shell-deny-by-default.md) | Shell deny by default |
| [0011](0011-harness-receives-normalized-input.md) | Harness receives normalized input |
| [0012](0012-model-adapters-own-provider-protocol.md) | Model adapters own provider protocol |
| [0013](0013-todo-tools-are-core.md) | Todo tools are core |
| [0014](0014-plan-execute-is-a-preset.md) | Plan-execute is a preset |
| [0015](0015-approval-is-core-state.md) | Approval is core state |
| [0016](0016-no-cross-run-approval-in-mvp.md) | No cross-run approval in MVP |
| [0017](0017-subagent-is-runtime-tool.md) | Subagent is runtime tool |
| [0018](0018-nested-subagents-disabled-by-default.md) | Nested subagents disabled by default |
| [0019](0019-sandbox-is-adapter.md) | Sandbox is adapter |
| [0020](0020-no-silent-sandbox-fallback.md) | No silent sandbox fallback |
| [0021](0021-mvp-does-not-require-subagents-or-sandbox.md) | MVP does not require subagents or sandbox |
| [0022](0022-agent-skills-progressive-disclosure.md) | Agent Skills use progressive disclosure |
| [0023](0023-context-management-retry-and-compaction.md) | Context management: classified retry and durable compaction |
| [0024](0024-hierarchical-instructions-and-environment-context.md) | Hierarchical instructions and environment context |
| [0025](0025-sandbox-escalation-ladder.md) | Sandbox escalation ladder |
| [0026](0026-user-questions-and-tool-result-guardrails.md) | User questions and tool-result guardrails |
| [0027](0027-token-meter-search-caps-turn-diffs.md) | Token meter, search discovery caps, and turn diffs |
| [0028](0028-deliverables-titles-and-write-observation.md) | Deliverables, session titles, and the write observation policy |
| [0029](0029-ordered-prompt-sections.md) | Ordered prompt sections with strict variable interpolation |
| [0030](0030-declared-tool-timeouts-and-deferred-loading.md) | Declared tool timeouts and deferred tool loading |
| [0031](0031-headless-exec-protocol.md) | Headless exec protocol (`--json`, `--output-schema`) |
| [0032](0032-layered-configuration-and-redaction.md) | Layered configuration, managed requirements, and redacted secrets |
| [0033](0033-apply-patch-envelope-tool.md) | The `apply_patch` envelope tool |
| [0034](0034-web-tools-and-ssrf-defense.md) | Web tools and the SSRF-safe fetch transport |
| [0035](0035-run-time-travel.md) | Run time travel: checkpoint fork and append-only revert |
| [0036](0036-plan-mode.md) | Plan mode: read-only until the plan is approved |
| [0037](0037-goals-and-ralph.md) | Goals and Ralph: a persisted objective and fresh-agent rounds |
| [0038](0038-seatbelt-sandbox.md) | macOS Seatbelt sandbox: a generated SBPL profile per session |
| [0039](0039-bubblewrap-sandbox.md) | Linux bubblewrap sandbox: the policy is the argument list |
| [0040](0040-landlock-planner.md) | Landlock: an ABI-aware planner plus a Linux-only applier |
| [0041](0041-seccomp-filter.md) | Seccomp: a planned BPF program, verified by a test-local interpreter |
| [0042](0042-restrict-then-exec-helper.md) | The Linux sandbox runs a restrict-then-exec helper (this binary) |
| [0043](0043-background-jobs.md) | Background jobs: detached processes with offset-addressed output |
| [0044](0044-hooks-engine.md) | Hooks: exit-code and JSON decisions, fail-open by default |
| [0045](0045-hooks-in-the-agent-loop.md) | Hooks in the agent loop: frozen run context and bounded Stop refusals |
| [0046](0046-landlock-file-rules.md) | Landlock rules for files, the safe device default, and syscall-based socket tests |
| [0047](0047-durable-memories.md) | Durable memories: readable files, content identity, frozen injection |
| [0048](0048-guardian-review.md) | A guardian review at the stop boundary: report by default, bounded enforcement |
| [0049](0049-commands-and-schedules.md) | Canned commands and unattended schedules: ordered expansion, policy-bound shell |
| [0050](0050-images-and-reasoning-replay.md) | Images as message content, and reasoning replayed with its signature |
| [0051](0051-seccomp-errno-and-unix-socketpairs.md) | A denied syscall returns EPERM, and AF_UNIX means socketpair |
| [0052](0052-pinning-linux-only-abi-numbers.md) | Linux-only ABI numbers are pinned on every platform |
| [0053](0053-mcp-server-mode-and-stdio-framing.md) | An MCP server, and the stdio framing the spec actually uses |
| [0054](0054-configured-mcp-servers-and-approval.md) | Configured MCP servers, gated by what the server declares |
| [0055](0055-mcp-stdio-dispatch-by-id.md) | The stdio client dispatches by id, so a deadline is real |
| [0056](0056-run-starting-mcp-tool-and-served-run-approval.md) | A run-starting MCP tool is gated three times, and a served run never prompts |
| [0057](0057-workflow-engine-javascript-sandbox.md) | The workflow engine is a JavaScript sandbox inside the Go process |
| [0058](0058-job-terminal-after-output-drain.md) | A job is terminal only after its output is drained |
| [0059](0059-workflow-tool-wiring.md) | A workflow script drives sub-agent runs through the orchestrator |
| [0060](0060-terminal-jobs-and-head-tail-buffers.md) | A terminal job is a session, and a bounded buffer keeps both ends |
| [0061](0061-mcp-per-server-time-bounds.md) | MCP server time bounds belong to the server entry |
| [0062](0062-standing-approval-is-a-rule.md) | A standing approval is a rule, not an argument string |
| [0063](0063-grants-list-and-revoke.md) | A standing grant an operator can see and take back |
| [0064](0064-a-child-can-name-its-model.md) | A child can run on a model it names |
| [0065](0065-a-structured-answer-is-checked.md) | A structured answer is checked, not trusted |
| [0066](0066-workflow-progress-is-its-own-event.md) | Workflow progress is its own event family |
| [0067](0067-a-served-run-can-outlive-its-call.md) | A served run can outlive the call that asked for it |
| [0068](0068-a-run-a-caller-can-stop.md) | A run a caller can stop |
| [0069](0069-a-schedule-outlives-the-process.md) | A schedule outlives the process that added it |
| [0070](0070-a-signed-webhook-starts-a-run.md) | A signed webhook starts a run |
| [0071](0071-resources-and-prompts-not-only-tools.md) | Resources and prompts, not only tools |
| [0072](0072-a-command-can-belong-to-a-person.md) | A command can belong to a person |
| [0073](0073-progress-is-a-notification.md) | Progress is a notification, not a second protocol |
| [0074](0074-a-server-can-ask-its-client.md) | A server can ask its client |
| [0075](0075-an-approval-can-be-a-question.md) | An approval can be a question |
| [0076](0076-a-resource-can-be-watched.md) | A resource can be watched |
| [0077](0077-the-client-can-lend-its-model.md) | The client can lend its model |
| [0080](0080-the-module-graph-is-validated.md) | The module graph is validated |
| [0099](0099-the-framework-core-and-the-console-adapter-are-separate-layers.md) | The framework core and the console adapter are separate layers |

## Console adapter (the DSH host)

These record how the optional DSH console host was built — the envelope
protocol, the streams, the settings namespaces it holds, the provider
directory and model selection it serves. They are one adapter's history; the
framework's own decisions are the table above. The tier and its dependency
rule are [ADR 0099](0099-the-framework-core-and-the-console-adapter-are-separate-layers.md),
and what the adapter answers today is
[the console coverage ledger](../dsh-console-coverage.md).

| Number | Topic |
| --- | --- |
| [0078](0078-a-console-on-localhost.md) | A console on localhost |
| [0079](0079-a-console-under-our-own-name.md) | A console under our own name |
| [0081](0081-the-console-answers-in-envelopes.md) | The console answers in envelopes |
| [0082](0082-the-host-names-what-it-cannot-answer.md) | The host names the model and records what it cannot answer |
| [0083](0083-the-streams-are-mounted-with-the-console.md) | The streams are mounted with the console |
| [0084](0084-one-credential-that-stays-write-only.md) | One credential, and it stays write-only |
| [0085](0085-the-provider-directory-is-served-discovery-is-not.md) | The provider directory is served, discovery is not |
| [0086](0086-a-session-outlives-its-runs.md) | A session outlives its runs |
| [0087](0087-the-settings-panel-is-served-with-a-real-schema.md) | The settings panel is served with a real schema |
| [0088](0088-the-preset-selectors-answer-with-the-hosts-own-settings.md) | The preset selectors answer with the host's own settings |
| [0089](0089-the-console-browses-the-workspace-read-only.md) | The console browses the workspace read-only |
| [0090](0090-the-consoles-slash-commands-are-the-hosts-commands.md) | The console's slash commands are the host's commands |
| [0091](0091-the-plugin-inventory-describes-what-this-host-publishes.md) | The plugin inventory describes what this host publishes |
| [0092](0092-the-console-shows-the-products-name-in-its-command-form.md) | The console shows the product's name in its command form |
| [0093](0093-the-first-party-console-is-deleted.md) | The first-party console is deleted |
| [0094](0094-the-host-holds-the-consoles-own-settings-namespaces.md) | The host holds the console's own settings namespaces |
| [0095](0095-the-host-holds-hand-declared-provider-profiles.md) | The host holds hand-declared provider profiles |
| [0096](0096-the-consoles-model-selection-is-served-per-session.md) | The console's model selection is served, per session |
| [0097](0097-the-consoles-model-discovery-interrogates-the-draft-endpoint.md) | The console's model discovery interrogates the draft endpoint |
| [0098](0098-a-declared-profile-is-edited-field-by-field.md) | A declared profile is edited field by field |
| [0100](0100-the-directory-picker-serves-the-browse-half-and-refuses-the-native-half.md) | The directory picker serves the browse half and refuses the native half |
| [0101](0101-the-console-workspace-registry-is-process-local.md) | The console's workspace registry is process-local, and sessions run in the host's one directory |
| [0102](0102-the-consoles-settings-and-credential-persist-in-a-host-owned-file.md) | The console's settings and credential persist in a host-owned file |
| [0103](0103-the-user-layer-is-what-the-console-wrote-and-a-chosen-model-persists.md) | The user layer is what the console wrote, and a chosen model persists |
| [0104](0104-a-session-exists-before-its-first-turn.md) | A session exists before its first turn, and a registered session has not chosen |
| [0105](0105-the-durable-log-is-projected-into-the-consoles-vocabulary.md) | The durable log is projected into the console's session vocabulary |
| [0106](0106-a-session-title-names-the-operators-task.md) | A session's title names the operator's task, not the preset's instruction |
| [0107](0107-plan-execute-plans-only-when-the-request-needs-one.md) | Plan-execute plans only when the request needs a plan |
| [0108](0108-a-sessions-log-is-one-sequence-across-its-turns.md) | A session's log is one sequence across its turns |
| [0109](0109-the-console-host-keeps-its-run-registry-on-disk.md) | The console host keeps its run registry on disk |
| [0110](0110-a-projected-message-is-identified-by-the-session-sequence.md) | A projected message is identified by the session sequence, not the run's |
| [0111](0111-a-run-records-the-prompt-identity-its-caller-submitted.md) | A run records the prompt identity its caller submitted |
| [0112](0112-the-coverage-ledgers-gaps-are-a-dated-audit.md) | The coverage ledger's gaps are a dated audit |
| [0113](0113-cancel-reaches-the-turn-that-is-running.md) | Cancel reaches the turn that is running |
| [0114](0114-a-follow-stream-follows-the-conversation.md) | A follow stream follows the conversation, not one turn |
| [0115](0115-an-approval-names-the-conversation.md) | An approval names the conversation, not the turn |
| [0116](0116-the-answer-streams-as-assistant-frames.md) | The answer streams as assistant-stream frames |
| [0117](0117-the-session-log-carries-the-console-vocabulary.md) | The session log carries the console's vocabulary |
| [0118](0118-a-reconnect-mid-answer-resumes-the-attempt.md) | A reconnect mid-answer resumes the attempt |
| [0119](0119-the-console-baselines-are-complete.md) | The console's baselines are complete |
| [0120](0120-the-file-surfaces-the-console-reads.md) | The file surfaces the console reads |
| [0121](0121-the-turn-and-step-boundaries-the-console-reads.md) | The turn and step boundaries the console reads |
| [0122](0122-the-built-in-routes-are-editable-cards.md) | The built-in routes are editable cards |
| [0123](0123-the-prompt-cards-the-console-renders.md) | The prompt cards the console renders |
| [0124](0124-the-chunks-and-errors-the-console-reads-off-a-stream.md) | The chunks and errors the console reads off a stream |
| [0125](0125-the-goal-dock-reads-the-frameworks-goal-state.md) | The goal dock reads and mutates the framework's goal state |
| [0126](0126-a-desktop-capability-is-answered-not-built.md) | A desktop capability is answered, not built |
| [0127](0127-the-sidebars-search-reads-the-logs-the-list-shows.md) | The sidebar's search reads the logs the list already shows |
