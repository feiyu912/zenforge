// Package jsonl provides a JSONL-backed event log implementation.
//
// Read and LatestSeq use json.Decoder like the source platform storage code, so
// they can read both one-object-per-line JSONL and pretty-printed JSON objects.
// Corrupt JSON returns an explicit parse error.
package jsonl
