---
name: project-orientation
description: Inspect a Go repository and summarize its structure using workspace evidence.
license: Apache-2.0
compatibility: ZenForge harness-agent
---
# Project orientation

Use `inspect_path` to classify relevant workspace-relative paths without reading
their contents. Use the sandboxed `shell` tool only when file contents or
repository commands are needed.

Base the answer on observed evidence. Distinguish files from directories, call
out the main Go packages, and keep the summary concise.
