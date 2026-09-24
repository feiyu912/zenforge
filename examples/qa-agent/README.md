# Q&A agent

This is a small external-application-style agent that answers a question over a
workspace. It advertises Agent Skills from a filesystem catalog, loads one
skill's instructions on demand, and inspects the workspace with a shell tool
that asks the operator to approve every command. The shipped
`skills/qa-evidence-lookup/SKILL.md` is a real instruction package: only its
name and description reach the prompt, and the body is disclosed when the model
calls `load_skill`.

Set the provider protocol and matching endpoint credentials:

```sh
export ZENFORGE_PROVIDER=openai # or anthropic
export ZENFORGE_MODEL=your-model
export ZENFORGE_API_KEY=your-key
export ZENFORGE_BASE_URL=https://your-endpoint.example/v1

go run ./examples/qa-agent -question "What does the workspace evidence say?"
```

The default workspace is `.`; `-workspace /path/to/repo` inspects another tree.
The default skill root is `skills`, resolved against the current directory;
override it with `-skill-root /path/to/skills` or
`ZENFORGE_SKILL_ROOT=/path/to/skills`.

The shell backend defaults to Docker (`-sandbox docker`, image
`-image alpine:3.20`), which mounts the workspace read-only at `/workspace`.
`-sandbox local` runs the command in the workspace itself and needs nothing
installed.

Without `-question`, the first stdin line is the question. Later stdin lines
remain available for CLI approval choices: the broker prints numbered options
(`1. Approve`, `2. Reject`) and reads one number.

Every observable step is one stdout line, so a transcript is greppable:

```text
skill: loaded qa-evidence-lookup
tool: load_skill
tool: shell
approval: shell approve
answer: ...
```

Which SDK call carries each part:

- `skillfs.New` and `skill.NewBundle` build the catalog prompt and the
  `load_skill` tool; `zenforge.Config.Skills` wires both into the run.
- `shelltool.Must` takes a `policy.ShellPolicy` with `RequireApproval: true`.
  `policy.ReviewCommand` sends a command that matches no allowlist entry to the
  approval broker, so leaving `AllowCommands` empty means every command is
  approved first.
- `approvalcli.New` owns the prompt on stderr; a small wrapper broker in
  `main.go` prints the `approval:` line from the decision the CLI returned.
- A `tool.Middleware` on `zenforge.Config.ToolRuntime` prints one `tool:` line
  per model-requested call.

What the test does differently:

`main_test.go` builds the example and runs it as a child process against
`modelstub`, a scripted OpenAI-compatible endpoint on a loopback port, so the
test needs no provider credential, no network, no Docker and no TTY. It passes
`-sandbox local` and feeds `1` (Approve) on stdin. Only the model's words are
scripted; the provider adapter, agent loop, filesystem skill catalog, shell
tool, approval broker and transcript are the real ones. The test asserts that
the first request carries the skill's description but not its body (progressive
disclosure), that the loaded body and the approved command's stdout both reach
the model (`Request.Delivered`), and that stdout carries
`answer: <scripted content>`. A second test, gated on
`ZENFORGE_DOCKER_INTEGRATION=1`, runs the same path with `-sandbox docker` and
asserts the command ran inside the container.