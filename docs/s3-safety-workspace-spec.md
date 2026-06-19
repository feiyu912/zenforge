# S3 Safety And Workspace Spec

S3 builds safe local work capabilities: workspace file access and shell
execution.

The goal is to let an agent inspect and modify a bounded workspace, run
approved commands, and request approval when an operation exceeds configured
policy.

## S3 Outcome

After S3, ZenForge should have:

- `workspace.Workspace` implementations;
- local workspace with path jail;
- file read/list/grep/write tools;
- shell tool;
- command allowlist;
- bash safety review;
- file access plan;
- read-before-write snapshots;
- approval request integration points;
- tests for path traversal, symlinks, command policy, and write staleness.

## Design Principles

1. Workspace is an interface, not always local disk.
2. File policy is separate from file storage.
3. Shell execution is denied by default unless configured.
4. Risky operations produce approval requests, not silent execution.
5. Tools must have output limits.
6. The default setup should be useful for code/repo agents.

## Package Plan

```text
workspace/
  interface.go
  local/
  memory/

policy/
  file.go
  shell.go
  approval.go

tools/workspace/
  read.go
  write.go
  list.go
  grep.go

tools/shell/
  shell.go
  local.go

safety/bashast/
safety/bashsec/
```

`safety/bashast` and the complete `safety/bashsec` validator set are ported from
`agent-platform` behind standalone ZenForge types. The AST layer uses
`mvdan.cc/sh/v3`; the security layer preserves platform AST, legacy fallback,
wrapper-command, redirection, and embedded-script classification without
importing platform contracts.

## Workspace Interface

```go
type Workspace interface {
    Read(ctx context.Context, path string) ([]byte, error)
    Write(ctx context.Context, path string, data []byte) error
    List(ctx context.Context, path string) ([]FileInfo, error)
    Grep(ctx context.Context, query GrepQuery) ([]Match, error)
    Stat(ctx context.Context, path string) (FileInfo, error)
}
```

## Local Workspace

```go
type LocalConfig struct {
    Root          string
    FollowSymlink bool
    MaxReadBytes  int64
    MaxWriteBytes int64
    AllowBinaryRead bool
}
```

Rules:

- all paths resolve under `Root`;
- `..` traversal is blocked after clean/eval;
- symlinks escaping root are blocked;
- device files are blocked;
- binary reads are blocked by platform extension classification and NUL-byte
  detection unless `AllowBinaryRead` is explicitly enabled;
- writes create parent directories only when configured.

## File Policy

```go
type FilePolicy struct {
    ReadRoots       []string
    WriteRoots      []string
    RequireApproval bool
}
```

Access plan:

```go
type FileAccessPlan struct {
    Operation        FileOperation
    RawPath          string
    Path             string
    Root             string
    Allowed          bool
    RequiresApproval bool
    RuleKey          string
    Fingerprint string
    Reason     string
}
```

Write plan:

```go
type FileWritePlan struct {
    FilePath    string
    Root        string
    SizeBytes   int64
    SHA256      string
    Description string
    Fingerprint string
    RuleKey     string
}
```

## Read-Before-Write

To reduce accidental overwrites, writes can require a fresh read snapshot.

```go
type ReadSnapshot struct {
    Path           string
    ModifiedUnixMs int64
    SizeBytes      int64
    SHA256         string
    ReadAt         time.Time
}
```

Write validation:

- if file exists and has not been read in this run, require approval or fail;
- if file changed since read snapshot, require approval or fail;
- new files can be allowed under write root.

MVP mode:

- warn/approval for stale writes;
- strict mode later.

The workspace tools expose the MVP strict path through
`tools/workspace.SnapshotStore`. `workspace_read` records file metadata after a
successful read. When `RequireReadBeforeWrite` is enabled, `workspace_write`
requires a matching snapshot for existing files and fails stale writes if size,
mtime, SHA256, or file type changed since the read. Snapshots are scoped by
run ID, so a read from one run cannot authorize a write in another run.

## Current Implementation Notes

- `policy.PlanFileAccess` normalizes workspace-relative paths, matches
  configured read/write roots, and returns an allow, deny, or approval-required
  plan with a stable rule key and fingerprint.
- `policy.PlanFileWrite` adds content SHA256 and a content-sensitive
  fingerprint for write approvals.
- `tools/workspace.Config.Policy` wraps `workspace_read`, `workspace_list`,
  `workspace_grep`, and `workspace_write` before adapter access. Denied file
  operations return `policy.ErrFileAccessDenied` with the access plan in the
  structured result.
- Approval-required file operations return an `approval.Request` payload with
  `accessPlan`, `writePlan` for writes, `fingerprint`, and `ruleKey`.
  Decisions replay through the normal approval metadata keys.
- `workspace/local` rejects final symlink write escapes and non-regular targets
  before writing. Grep skips non-regular files.

## Workspace Tools

Tool names:

- `workspace_read`
- `workspace_write`
- `workspace_list`
- `workspace_grep`

Compatibility aliases can be added later:

- `file_read`
- `file_write`
- `file_grep`

### Read Tool

Input:

```json
{
  "path": "README.md",
  "offset": 0,
  "limit": 20000
}
```

Output:

- text content;
- path metadata;
- snapshot metadata.

### Write Tool

Input:

```json
{
  "path": "notes.md",
  "content": "...",
  "description": "why this write is needed"
}
```

Rules:

- description required;
- content size limited;
- policy checked before write;
- read-before-write checked where applicable;
- emits `workspace.changed`.

### Grep Tool

Input:

```json
{
  "pattern": "TODO",
  "path": ".",
  "maxMatches": 100
}
```

Rules:

- path must be readable;
- output capped;
- binary files skipped.

## Shell Tool

Tool name:

- `shell`

Compatibility alias later:

- `bash`

Input:

```json
{
  "command": "go test ./...",
  "cwd": ".",
  "timeoutMs": 30000,
  "description": "run test suite"
}
```

Rules:

- `description` required;
- command must pass allowlist or safety review;
- cwd must be inside workspace or configured root;
- timeout required/defaulted;
- output capped;
- env allowlist configurable;
- secrets redacted from event/log output.

## Shell Policy

```go
type ShellPolicy struct {
    WorkingDir       string
    AllowCommands    []string
    DenyCommands     []string
    RequireApproval  bool
    MaxTimeout       time.Duration
    MaxOutputBytes   int64
    Env              map[string]string
    AllowedEnvKeys   []string
}
```

Command review result:

```go
type CommandReview struct {
    Decision    ReviewDecision
    Reason      string
    RuleKey     string
    Fingerprint string
    Risk        string
}
```

Decisions:

- allow;
- require approval;
- block.

Current shell review parses Bash before applying allow/deny rules. Every command
found in a chain, pipeline, or substitution must independently match an allow
rule. Output redirects and statically unresolved targets require approval;
sensitive input redirects, dangerous builtins, ambiguous syntax, wrappers, and
dangerous inline interpreter scripts are blocked. Syntax that cannot be
analyzed safely requires approval when configured and is denied otherwise.

## Approval Integration

S3 should not implement the full approval broker. It should produce structured
approval requests that S6 will route.

Temporary S3 behavior:

- with no broker: return `approval_required` tool error;
- with broker: pause and resume.

Approval request payloads should include:

- operation;
- path or command;
- reason;
- rule key;
- fingerprint;
- risk;
- proposed scope.

## Events

S3 tools emit:

- `tool.call`;
- `tool.result`;
- `tool.error`;
- `workspace.changed`;
- `approval.requested` when policy blocks direct execution.

## Migration From agent-platform

Strong candidates to port:

- `internal/bashast`;
- `internal/bashsec`;
- `internal/filetools/filetools.go`;
- `internal/filetools/binary.go`;
- `internal/tools/tool_file.go` behavior;
- `internal/tools/tool_grep.go` behavior;
- `internal/tools/tool_bash.go` policy concepts.

Do not port directly:

- `contracts.ExecutionContext`;
- `config.FileToolsConfig`;
- `config.BashConfig`;
- ZenMind approval maps on `ExecutionContext`;
- chat-specific artifact/resource behavior.

## S3 Tests

Minimum tests:

- local workspace blocks `../` traversal;
- symlink escape blocked;
- read/list/grep under root works;
- binary/device file handling;
- write under write root works;
- write outside root requires approval/error;
- stale write detection;
- shell command allowlist passes;
- denied shell command blocked;
- timeout kills command;
- max output enforced;
- shell cwd escape blocked;
- approval-required result shape.

## S3 Exit Criteria

- file and shell tools are usable without model runtime;
- safety policy is tested;
- approval request shape is ready for S6;
- no ZenMind platform imports;
- S4 harness can include these tools by default.
