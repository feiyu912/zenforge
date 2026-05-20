# S3 Implementation Plan

This document breaks `s3-safety-workspace-spec.md` into implementation tasks.

## 1. Workspace Interface Update

Update `workspace.Workspace` to include:

- `Stat`;
- stronger `FileInfo`;
- `GrepQuery`;
- `Match`.

Acceptance:

- package compiles;
- existing root config still compiles.

## 2. Local Workspace

Create:

```text
workspace/local/
```

Implement:

- root resolution;
- path cleaning;
- symlink evaluation;
- read/write/list/stat;
- grep.

Acceptance:

- temp-dir tests;
- path traversal blocked;
- symlink escape blocked.

## 3. File Policy

Create:

```text
policy/file.go
```

Implement:

- `FilePolicy`;
- `FileAccessPlan`;
- `FileWritePlan`;
- rule key/fingerprint;
- read/write root matching.

Acceptance:

- allowed and denied roots tested;
- fingerprint stable;
- rule key stable per root/operation.

## 4. Read Snapshot Store

Implement simple in-run snapshot tracker.

Acceptance:

- read records snapshot;
- write validates snapshot;
- stale snapshot detected.

## 5. Workspace Tools

Create:

```text
tools/workspace/
```

Implement:

- read;
- write;
- list;
- grep.

Acceptance:

- tools satisfy `tool.Tool`;
- tool tests use local workspace;
- write emits workspace change metadata in result.

## 6. Shell Policy

Create:

```text
policy/shell.go
```

Implement:

- allowlist;
- denylist;
- timeout limit;
- cwd validation;
- env filtering.

Acceptance:

- allowed command passes;
- denied command blocked;
- cwd escape blocked.

## 7. Port Bash Parser/Security

Create:

```text
safety/bashast/
safety/bashsec/
```

Port from `agent-platform` with package names adjusted.

Acceptance:

- original relevant tests ported;
- no imports from `agent-platform`;
- command review returns allow/approval/block.

## 8. Shell Tool

Create:

```text
tools/shell/
```

Implement:

- command execution;
- timeout;
- output cap;
- env;
- cwd;
- policy review.

Acceptance:

- no command runs without policy;
- timeout test;
- output cap test;
- approval-required result test.

## 9. Documentation

Add user-facing docs:

- workspace tools;
- shell safety;
- approval-required behavior.

## Done Criteria

- `go test ./...` passes;
- S3 tests cover file and command safety;
- tools are usable through S2 invoker;
- S4 can use workspace and shell as default optional tools.

