# 0142. The scenario examples run end to end: a scripted endpoint drives the real path, and the two defects the examples surfaced are fixed

- Status: Accepted
- Date: 2026-09-24
- Related: 0022 (Agent Skills progressive disclosure), 0020 (no silent fallback
  from a sandbox to local execution), 0099 (the framework core and the console
  adapter are separate layers), 0137 (the verification recipe is enforced), 0141
  (the sibling chain), 0078 (the loopback HTTP host), 0035 (run time travel)

## Context

The repository shipped six examples, and the acceptance suite's claim about them
was weaker than it read. Four of the six were only *scanned*:
`examples/examples_test.go` asserts that a source file contains a string such as
`RequireApproval: true` or `provider.FromEnv()`, which is a claim about what the
file says rather than about what it does. Every provider-backed example needed a
real credential, so `go test ./examples/...` -- a step in the default CI job --
never executed the harness's real path through an example: the provider adapter,
the tool loop, the approval broker, the skill loader and the checkpoint store
were all exercised somewhere in the repository, but never by a program shaped the
way an application is shaped.

Each of the three scenarios the product brief asks for was half-covered at best.
`examples/harness-agent` has no test file at all, and its skill is validated only
because the HTTP example points `-skill-root` at it. No example anywhere called
`Agent.Resume`: the only occurrence of "resume" under `examples/` was an HTTP
route registration. The single example that edits a workspace
(`examples/code-review-agent`) sets `MaxWriteBytes: 1`, so every real write
fails; nothing in the tree showed an editing agent whose approvals are enforced.

An example that has never run is documentation, not evidence.

## Decision

### 1. Three examples, each a scenario, each runnable

`examples/qa-agent`, `examples/long-task-agent` and `examples/coding-agent` are
new. Each is a single `main.go` plus a `README.md`, builds its model with
`provider.FromEnv()`, parses flags, prints a one-line-per-step transcript on
stdout, and prints errors as `<name>: <err>` on stderr with exit status 1 -- the
shape `examples/harness-agent` established.

- `qa-agent`: question answering with a filesystem Agent Skill the model loads
  on demand, a shell tool, and HITL approval on every command. `-sandbox docker`
  (the default) runs the command in `alpine:3.20` with the workspace mounted
  read-only at `/workspace`; `-sandbox local` runs it on the host.
- `long-task-agent`: a durable run that pauses, exits, and is resumed by a second
  process from its checkpoint.
- `coding-agent`: an agent that reads a file, edits it, and runs a command, with
  every write and every non-allowlisted command gated behind the operator.

### 2. The scripted endpoint lives in the test, not in the example

CI has no provider credential, so the examples do not bend to make themselves
testable: their tests point the four `ZENFORGE_*` variables at a loopback
OpenAI-compatible endpoint and run the built binary as a child process, exactly
as a user runs it. The endpoint is `examples/internal/modelstub`, under
`examples/internal/` so nothing outside `examples/` can import it and a
`_test.go` file is the only thing that may.

The consequence is that the tests exercise the **real** OpenAI adapter over HTTP,
the real streaming wire format, the real tool schemas, the real approval broker,
the real skill catalog and the real JSONL checkpoint store. Only the model's
words are scripted. The rejected alternative was an in-process fake
`model.Model` -- the pattern `examples/sdk-embedded-agent` uses. It is faster and
needs no server, but it skips the adapter and the wire format, which is the part
of a deployment that breaks first, and it cannot drive an example that is tested
as a process. The repository had no exported fake model to reuse for either
purpose; this ADR adds a fixture rather than a public fake, because a public
scripted model would invite applications to test against it instead of against
their provider.

### 3. What each scenario's test proves

Assertions are made on the child's stdout and stderr, on the requests the
endpoint recorded, and on the filesystem -- never on the example's source text.

- `qa-agent`: the first model request carries the skill's name and description
  and **not** its body (progressive disclosure, ADR 0022); the body reaches the
  model only after `load_skill` ran and its result was delivered; the operator's
  approval was prompted on stderr and accepted; the approved command's stdout
  reached the model, and the transcript printed one `tool: shell` line for the
  one call the model requested even though an approval makes the agent re-invoke
  it.
- `long-task-agent`: the first process ends `run: incomplete` with the run id and
  leaves a checkpoint on disk; a second process resumes that run id, prints
  `run: resumed` and finishes `run: done`; the model requests made during the
  resumed run still carry the text recorded before the interruption, which is
  what distinguishes a resume from a restart.
- `coding-agent`: the file on disk really changed; the operator was prompted for
  both the write and the command; a read result preceded the write call in the
  model's requests; the command's output reached the model; and, in a second
  test, a rejected approval leaves the file byte-identical, produces no `tool:` or
  `write:` line, and reaches the model as `approval_rejected` while the run still
  finishes.

### 4. The interruption in the long task is an approval pause, not a step limit

`MaxSteps` exhaustion is not a resumable stop: at the limit the runner appends
the tool-use-limit message, makes one final no-tool model call and completes the
run (`harness/runner.go`), so `Resume` on it merely replays `run.done`. The one
public-API interruption that leaves a resumable checkpoint is a tool that
requires approval with `Config.Approval` unset: the run records the waiting
request, emits `approval.requested`, and returns `approval.ErrRequired` with the
run id, leaving the checkpoint at the approval phase. The example uses that, and
its README says so, because an example that claimed to demonstrate resume from a
step limit would be demonstrating something the SDK does not do.

### 5. HITL is driven by numbering, and one buffer serves every prompt

`approval/cli` renders numbered options and reads an option number (`1` approve,
`2` reject); it does not accept yes/no. The example tests answer with the numbers
a person types.

Reading that path in anger found a defect: the broker constructed a fresh
`bufio.Reader` for every prompt, so the first prompt's read-ahead consumed
answers written ahead of time and the second prompt read EOF. A terminal operator
was unaffected; any scripted or piped operator -- exactly what a CI job driving
an approval-gated example is -- failed on the second prompt. `approval/cli` now
reads every prompt through one shared buffer, and two tests pin it (one of them
fails with `prompt 2 returned error: EOF` when the old behaviour is restored).

### 6. The Docker path is opt-in, and CI now runs it

`qa-agent`'s Docker test skips unless `ZENFORGE_DOCKER_INTEGRATION=1`, matching
the existing convention, because the default CI job has no daemon. The Docker CI
job now runs it alongside the HTTP example's Docker test, and it also runs
`sandbox/docker`'s own integration test, which no CI job executed before.

### 7. The defects the examples surfaced are fixed in this chain

Writing an example that mounts a workspace into a container, and a test that
drives two approvals, exposed two real defects. Both are fixed here rather than
recorded as debts:

- **`--sandbox-root` was a no-op for the Docker backend.** The flag is documented
  as setting the writable roots, "the shell working directory by default", and
  the other backends honour it; the Docker branch ignored the roots entirely and
  set no mounts, so the model's shell ran in a container with an empty working
  directory and could not read the project at all. `cli/sandbox.go` now resolves
  the roots once (shared by both callers) and `cli/shelltool.go` turns them into
  bind mounts at their host paths -- which is also what makes the shell tool's
  host-to-container path mapping work -- read-write unless `--sandbox-restricted`
  asked for the read-only layout. `TestDockerSandboxMountsTheWritableRoots` pins
  the mounts, the mode, the duplicate handling, and that the other backends still
  get none.
- **One buffered reader per approval prompt**, described in §5.

### 8. The older examples stay, and one stale claim is corrected

The six older examples keep their place: each shows one slice with a real
credential, and `harness-agent` remains the flagship assembly. Their static scans
are untouched -- this chain does not pretend to have retrofitted them. What it
does correct is the one claim that was simply false: `README.md` described
`repo-refactor-agent` as "Long task with checkpoints and resume", and that example
never resumed anything. It now says what it does, and the resume claim belongs to
`long-task-agent`, which does it.

### 9. The promise is checked, not asserted

`TestScenarioExamplesUseOnlyPublicSDKPaths` parses every `.go` file in the three
scenario directories and fails if one imports `cli`, `cmd/`, `internal/` or
`webui/`, or if a non-test file imports the scripted endpoint fixture. A scenario
example that quietly reached into the console adapter would stop being evidence
that an embedder can build the same thing from the public SDK.

## Consequences

- `go test ./examples/...`, a step in the default CI job, now builds and runs
  three real programs against a loopback endpoint. A break in the provider
  adapter's wire handling, a tool schema, the approval wiring or the checkpoint
  format now fails CI through an application-shaped program, not only through a
  package test.
- The example tests are slower than the scans they complement: each builds a
  binary and runs it, and they need a Go toolchain. They need no network, no
  credential, no TTY and, except for the gated Docker test, no daemon.
- Three documents that enumerate the examples, and the roadmap's MVP row, now
  describe nine examples instead of six and name which ones the acceptance suite
  runs.
- What the scripted endpoint cannot prove is unchanged: a real provider's
  behaviour. Live provider credentials remain an application-owned smoke test.
- The scripted endpoint is a test fixture with one consumer class, so it is
  deliberately unexported: `examples/internal/modelstub`.

## Verification

The recipe the repository enforces (ADR 0137), run from the repository root with
`GOCACHE` redirected to a temporary directory:

```text
$ gofmt -l <every tracked .go file except webui/>        # no output
$ go vet ./...                                           # clean
$ go test -race ./...                                    # green, except the two cases below
$ go test ./examples/...                                 # green
$ mkdocs build --strict                                  # green
$ cd integration/consumer \
  && go mod tidy && git diff --exit-code -- go.mod go.sum \
  && go test -race ./... && go vet ./...
ok  github.com/feiyu912/zenforge-consumer-test  8.030s
```

Exactly one package fails on this machine: `sandbox/seatbelt`, whose
`TestSeatbeltActuallyEnforcesTheProfile` reports `write outside the writable root
succeeded` because the shell this agent runs in grants full file access, so the
profile's refusal cannot be observed. It fails identically at 663173f, verified
with a throwaway worktree of that commit, and this chain touches neither the
package nor anything it depends on. The PTY failure ADR 0137 and ADR 0141
recorded for `tools/jobs` did **not** reproduce here: `go test -race -count=1
./tools/jobs/` passes in this run, so the local recipe now has one
environment-dependent case rather than two, and CI remains the authority for
both.

The three scenarios were also run by hand, the way a user runs them, against a
scripted endpoint written outside Go -- a small Python SSE server, which is a
second implementation of the wire format, so the adapter is not being graded by
its own fixture:

`qa-agent -sandbox local`, three model calls, one approval:

```text
skill: loaded qa-evidence-lookup
tool: load_skill
tool: shell
Approval required: Approve shell command
collect live evidence for the operator
Risk: high
1. Approve
2. Reject
> approval: shell approve
answer: The approved command printed qa-live-ok; the skill told me to answer from observed evidence.
```

The recorded requests show the contract rather than the transcript: request 1
carries `Available skills:` with the skill's description and not its body;
request 2's tool message carries the body and
`"digest":"sha256:c4c956247318c6063781818bb59f1c264f0a4e7882ff2988710babde06aa3382"`;
request 3 carries `qa-live-ok`.

`coding-agent` with **both** answers written into the child's stdin before the
first prompt appeared -- which only works because of the buffered-reader fix in
§5:

```text
tool: workspace_read
Approval required: Approve workspace write
1. Approve
2. Reject
> approval: workspace_write approve
tool: workspace_write
write: greeting.txt
Approval required: Approve shell command
> approval: shell approve
tool: shell
shell: printf 'check-ok\n'
answer: Updated greeting.txt and verified it with printf check-ok.
```

`greeting.txt` on disk afterwards contains `hello, live coding agent`, and the
run directory holds the JSONL event log. `qa-agent -sandbox docker` ran the same
transcript, and the recorded request carries the container's answer:

```json
{"command":"echo qa-live-ok && uname -s","output":"qa-live-ok\nLinux\n","backend":"sandbox","exitCode":0}
```

`long-task-agent`, two processes. The first exits 75 with the run id and the
resume command, after the checkpoint that records the waiting approval:

```text
tool: finalize_task
checkpoint: seq=18 phase=approval file=.../.runs/long_live_1/latest.json
approval: requested finalize_task
run: incomplete long_live_1
run: resume with: long-task-agent -run-dir ... -workspace ... -resume long_live_1
```

The run directory then holds `long_live_1/checkpoints.jsonl`,
`long_live_1/latest.json` and `long_live_1/events.jsonl`. The second process
reads that store, answers the pending decision, and finishes:

```text
run: resumed long_live_1
Approval required: Finalize the long task
1. Approve
2. Reject
> approval: finalize_task approve
tool: finalize_task
checkpoint: seq=28 phase=completed file=.../latest.json
run: done long_live_1
answer: The operator signed the report off; the long task is complete.
```

The report the gated tool wrote contains both steps recorded before the pause,
which is the on-disk form of the same claim the test makes on the wire.

The Docker-gated tests were run on this machine as well (Docker 29.4.1, image
`alpine:3.20`, already pulled):

```text
$ ZENFORGE_DOCKER_INTEGRATION=1 go test ./examples/qa-agent \
    -run '^TestQAAgentRunsApprovedDockerShellWhenDockerIsEnabled$'
--- PASS: TestQAAgentRunsApprovedDockerShellWhenDockerIsEnabled (2.22s)

$ ZENFORGE_DOCKER_INTEGRATION=1 go test ./sandbox/docker -run '^TestDockerIntegration$'
--- PASS: TestDockerIntegration (0.32s)
```

Both are also run by the Docker CI job, which now executes the new example test
and the adapter's own integration test as well.

## Deviations

No deviation is left open. Every change made outside the three example
directories is one of these, and each is explained above:

- the two defect fixes in §7 (`cli/sandbox.go` + `cli/shelltool.go` for the
  Docker mounts, `approval/cli/cli.go` for the shared reader), each with the test
  that fails without it;
- the Docker CI job, which now runs the new gated example test and also
  `sandbox/docker`'s own integration test -- the latter executed in no CI job
  before this chain, which is a hole in the under-testing direction;
- three root-anchored entries in `.gitignore` for the example binaries
  (`/qa-agent`, `/long-task-agent`, `/coding-agent`), because `go build
  ./examples/<name>/` run from the repository root twice left a stray binary
  named after the example during this work. The leading slash is deliberate: an
  unanchored pattern would ignore the example directories themselves;
- the documentation corrections in §8 and the stale-claim fixes the examples
  surfaced during review: `README.md`'s long-task row and `docs/quickstart.md`'s
  long-task command both described the interruption as a step limit, which §4
  shows the SDK does not support, so both now describe the approval pause.

Deliberately **not** done, so the boundary of this chain is explicit: the older
six examples keep their source-scanning tests rather than being retrofitted to
this pattern (their place is one slice each, with a real credential); no
benchmark or comparison against other agent frameworks is attempted here even
though it is the next item in the same plan; and no live-provider smoke test is
added, because a credential is exactly what CI does not have and what the
scripted endpoint deliberately replaces.