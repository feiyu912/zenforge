# Coding Agent

A workspace-editing agent with a human in the loop. It reads a file, makes a
minimal change to it, then runs a shell command to verify the change -- and the
operator is asked before each write and before any command the policy has not
allowlisted.

This is the scenario the read-mostly examples deliberately avoid. The workspace
is genuinely edited: writes are permitted, but no write root is pre-authorized,
so nothing reaches disk without an approval, and the test proves the result on
disk rather than in a transcript.

The transcript on stdout is one greppable line per observable step:

```
tool: workspace_read
approval: workspace_write approve
tool: workspace_write
write: greeting.txt
approval: shell approve
tool: shell
shell: printf 'check-ok\n'
answer: Updated greeting.txt and verified it with printf check-ok.
```

`approval:` records the operator's decision for the tool that asked; `tool:` and
the `write:`/`shell:` detail lines are printed only after the call really
executed, so a refused write leaves no `write:` line behind. The prompt itself
(the numbered choices) is printed on stderr by the CLI broker, keeping the
interaction separate from the transcript.

## Run it

The model comes from `provider.FromEnv()`, so the provider is configured with
`ZENFORGE_*` variables:

```bash
ZENFORGE_PROVIDER=openai \
ZENFORGE_MODEL=gpt-4.1 \
ZENFORGE_API_KEY=... \
ZENFORGE_BASE_URL=https://api.openai.com/v1 \
go run ./examples/coding-agent \
  -workspace . \
  -run-dir .zenforge/coding-agent \
  -task "Read greeting.txt, correct the greeting, and verify the change."
```

Flags:

| Flag | Default | Meaning |
| --- | --- | --- |
| `-task` | `Read greeting.txt, correct the greeting, and verify the change.` | The task handed to the agent. |
| `-workspace` | `.` | Workspace root the agent may read and edit. |
| `-run-dir` | `$ZENFORGE_RUN_DIR`, else `.zenforge/coding-agent` | JSONL event log and checkpoints, so a run is inspectable afterwards. |

Approval prompts are printed to stderr and read from stdin. The choice is the
**numbered option** the prompt shows (`1. Approve`, `2. Reject`), not yes/no.

## Which SDK call carries each guarantee

| Guarantee | Where it is configured |
| --- | --- |
| Reads and writes stay inside the workspace | `workspacelocal.New(workspacelocal.Config{Root: <abs workspace>})`, which resolves every path under an `os.OpenRoot` and refuses escapes |
| Every write asks the operator | `policy.FilePolicy{ReadRoots: []string{"."}, RequireApproval: true}` with **no write root**: `policy.PlanFileAccess` marks a write `RequiresApproval` whenever the write root list is empty, so no path is silently writable |
| An approved write actually happens | `tools/workspace` returns `approval.RequiredResult`; the agent loop re-runs the tool once `approval.MatchesApprovedMetadata` holds, and only then does the base tool touch the file |
| A write cannot blind-clobber a file | `workspacetools.Config{RequireReadBeforeWrite: true, Snapshots: workspacetools.NewSnapshotStore()}` refuses a write to a path this run has not read (and, for a new file, not observed as absent) |
| The shell is bounded and confined | `policy.ShellPolicy{WorkingDir: <abs workspace>, RequireApproval: true, MaxTimeout: 30 * time.Second, MaxOutputBytes: 1 << 20}` |
| Allowlisted commands skip the prompt | `policy.ShellPolicy.AllowCommands` (`go build ./...`, `go test ./...`); `policy.ReviewCommand` returns `allow` for them and `require_approval` for everything else |
| The operator decides | `approvalcli.New(os.Stdin, os.Stderr)`, wrapped so the decision is also part of the transcript |
| The run is inspectable | `eventlogjsonl.New(runDir)` and `checkpointjsonl.New(runDir)` |

`shelltool.ShellBackendLocal` is used so the example needs nothing installed
beyond the POSIX shell the agent already requires.

## What the test does differently

`main_test.go` does not scan this example's source. It builds the binary into a
temporary directory and runs it as a child process, so the flag parsing, the
approval broker, the tool policies and the exit code are all the real path.

Only the model is scripted: `examples/internal/modelstub` serves the same
`/v1/chat/completions` an OpenAI-compatible endpoint serves, on a loopback port,
and its `Env()` points `provider.FromEnv()` at it. The test therefore needs no
network, no provider credential, no Docker and no TTY.

Both the workspace and the run directory are `t.TempDir()`, seeded with one file
of known content. The test asserts three independent things:

1. **The file on disk.** After the run, `greeting.txt` holds the new content --
   the only proof that a write with approval really landed.
2. **What the model was told.** `modelstub.Request.Delivered` shows the read's
   result was in context before the model asked for the write, that the write
   and shell results came back afterwards, and that the shell command's output
   (`check-ok`) reached the model.
3. **The transcript.** stdout carries the `approval:`, `tool:`, `write:`,
   `shell:` and `answer:` lines; stderr carries the prompts.

Approvals are driven for real. The test plays the operator: it answers each
prompt with the numbered option once that prompt appears on the child's stderr,
which is what a person does and keeps the two streams in the order an
interactive session produces them. (Writing several answers ahead of time works
too: the broker reads every prompt through one shared buffer.)

### Approved run

`TestCodingAgentEditsAFileAndRunsAnApprovedCommand` scripts read, write, shell,
answer and feeds `1` at both prompts. It asserts the edit is on disk, both
prompts appeared, the `approval:`, `write:` and `shell:` lines are present, and
the read result preceded the write call in the requests the endpoint recorded.

### Denied run

`TestCodingAgentHonoursADeniedWrite` scripts read, write, answer and feeds `2`
at the write prompt. What the SDK really does with a rejection is the contract
this pins: the agent loop turns it into an `approval_rejected` tool error and
sends it back to the model (`agent.go` builds that result itself and never calls
the tool), the run continues to the scripted final answer and exits 0, and the
file is untouched. The transcript shows `approval: workspace_write reject` and
no `tool:` or `write:` line, and the next request to the model carries
`approval_rejected` -- so the refusal is reported honestly to both the operator
and the model.

## Verify

```bash
gofmt -l examples/coding-agent
go vet ./examples/coding-agent/
go test ./examples/coding-agent/ -count=1 -race
```
