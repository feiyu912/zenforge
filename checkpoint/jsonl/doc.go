// Package jsonl provides a JSONL-backed checkpoint store implementation.
//
// Load reads latest.json as the source of truth. checkpoints.jsonl is an
// append-only history stream and may contain older corrupt lines without
// preventing the latest checkpoint from loading.
package jsonl
