// Package compaction measures context-window pressure and durably compacts
// harness run-state messages before they exceed the configured model window.
//
// The design follows two production references:
//
//   - DeepSeek Harness compaction: pressure-triggered summarization at a
//     threshold ratio of the model window, retention of a recent-context
//     budget, model-free tool-result pruning with head/tail character
//     budgets, and bounded overflow-recovery retries.
//   - OpenAI Codex auto-compact: a handoff-summary prompt that addresses the
//     next model, a summary prefix installed as the replacement user
//     message, and compaction treated as a durable lifecycle (start,
//     summary, end) rather than an in-memory edit.
//
// Compaction is deterministic where it can be: pruning never calls a model,
// boundaries never split assistant tool calls from their results, and every
// compaction produces a harness.CompactionRecord persisted in run state so
// resume continues from the compacted history without re-summarizing.
package compaction
