package dshwire

// TitleProjection is the projection key a conversation's name is published under.
// The console reads the title from this projection cell -- a session list row
// seeds its projection store with it and the header folds it -- never from a
// field of its own, so the key belongs with the vocabulary rather than with one
// adapter (ADR 0119).
const TitleProjection = "title"

// KnownEventTypes is the console's own event vocabulary, copied from the
// vendored client's generated catalog
// (webui/dsh/plugins/api/session-controller/client.js KNOWN_SESSION_EVENT_TYPES).
// It exists for one decision: whether a passthrough record needs the `ignorable`
// marker. The console's read path refuses to interpret a log containing a type
// outside this set unless the event carries that marker -- an unmarked unknown
// type is treated as "written by a newer harness, and skipping it might
// reconstruct a wrong session" -- while a type the console knows must not be
// marked, or the console would skip an event it is supposed to read.
//
// vocabulary_test.go re-extracts the list from the bundle and fails when this
// table drifts from it, so a console upgrade cannot silently mis-mark a record.
var KnownEventTypes = map[string]bool{
	"agent-preset/selected":                  true,
	"agent/inbox/spliced":                    true,
	"approval/asked":                         true,
	"approval/decided":                       true,
	"approval/policy":                        true,
	"assistant/attempt":                      true,
	"assistant/message":                      true,
	"command/done":                           true,
	"command/run":                            true,
	"compaction/end":                         true,
	"compaction/prune":                       true,
	"compaction/start":                       true,
	"compaction/summary":                     true,
	"deliverables/presented":                 true,
	"feedback/message-delete":                true,
	"feedback/message-put":                   true,
	"feedback/record":                        true,
	"goal/change":                            true,
	"hook/invoked":                           true,
	"hook/result":                            true,
	"image/offload":                          true,
	"llm/retry":                              true,
	"llm/retry-started":                      true,
	"model/selection":                        true,
	"permission/preset":                      true,
	"plan/mode":                              true,
	"request/context":                        true,
	"request/header":                         true,
	"sandbox/mode":                           true,
	"schedule/change":                        true,
	"session-log-deepseek/delivery-accepted": true,
	"session/end-seed":                       true,
	"session/title":                          true,
	"session/title-llm-request":              true,
	"step/end":                               true,
	"step/start":                             true,
	"subagent/catalog":                       true,
	"subagent/descriptor":                    true,
	"subagent/model-selection-policy":        true,
	"system/message":                         true,
	"team/member":                            true,
	"team/message/delivered":                 true,
	"team/message/queued":                    true,
	"team/task":                              true,
	"todo/write":                             true,
	"tool-workflow/agent-end":                true,
	"tool-workflow/agent-start":              true,
	"tool-workflow/run-end":                  true,
	"tool-workflow/run-start":                true,
	"tool/call":                              true,
	"tool/ptc-dispatch":                      true,
	"tool/ptc-dispatch-start":                true,
	"tool/result":                            true,
	"turn/end":                               true,
	"turn/start":                             true,
	"user/message":                           true,
	"web/deepseek-search-llm-request":        true,
	"workspace/changes":                      true,
}
