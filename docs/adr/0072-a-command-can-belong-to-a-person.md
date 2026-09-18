# ADR 0072: A Command Can Belong To A Person

Status: accepted

## Context

The command catalog (ADR 0047) came from one place: `<workspace>/.zenforge/commands`.
A command is a canned task — "review the open changes", "commit with our message
style" — and the same person wants the same ones in every repository they open.
Copying them into each workspace means they drift, and the copy in the project's
repository is the version everyone else has to live with. C19's last item was
this gap: commands that belong to the person, not the project.

## Decision

### Two layers, and the workspace wins

The catalog is now loaded from two directories:

- the **workspace** layer: `<workspace>/.zenforge/commands` (unchanged default,
  still `--commands`),
- the **user** layer: `<user config dir>/zenforge/commands` — `$ZENFORGE_CONFIG_DIR/commands`,
  else `$XDG_CONFIG_HOME/zenforge/commands`, else `~/.config/zenforge/commands`,
  overridable with `--user-commands` and readable from the config file as
  `commands.userDir`.

The user layer is loaded first and the workspace layer overrides by command name,
including namespaced names from subdirectories, which both layers walk
identically. The project is the more specific intent: a repository that defines
`review` means *its* review, and a personal command must not silently change what
the project's own command does.

### Shadowing is visible, never silent

An overridden user command is kept, marked `Shadowed`, never resolved, and shown
in `--list-commands` directly under the command that replaced it, marked as such:

```
/review - Review the diff [workspace]
/review - Personal review [user] (workspace overrides user)
```

The listing also carries the layer for every command. A person whose edit "does
nothing" because a project command has the same name can see why in one line,
which is the difference between a precedence rule and a trap.

### A broken file says which layer it is in

A malformed command is still a loud error, and the error names the layer *and*
the absolute directory it came from. A typo in a personal file must not read like
a problem with the project, and vice versa: the first thing an operator asks is
which of their two files is wrong.

A user directory that does not exist is not an error and leaves the catalog
exactly as it was — most installs have no personal commands and must not be told
about it.

### The default config file does not advertise it

`commands.userDir` is omitted from the default config file (`zenforge init`),
because the default is a resolved path rather than a value worth writing into a
config: the field exists to *override* a convention, and a documented default
path in a generated file would freeze this machine's home directory into the
project's config. It is documented here, in the parity row, and in the reference
prose instead.

## Consequences

- C19 is complete. Every trigger and command surface the row lists now exists:
  commands, the in-process loop, durable schedules (ADR 0069), the signed webhook
  (ADR 0070), and commands that belong to a person.
- The MCP prompt surface (ADR 0071) picks the merged catalog up for free: a
  personal command is offered as a prompt in every workspace.
- Two files can define the same name and the loser is still readable in the
  listing, so precedence is a documented rule rather than a hidden winner.
- The config reference pins the default config file verbatim, so a field that is
  deliberately absent from that file is documented outside the pinned block;
  adding it later means changing both in the same commit.

## Alternatives Rejected

### User commands win over workspace commands

Then installing a personal `review` would change what a project's own `review`
does, and a repository could not ship a command it depends on. The specific
place wins over the general habit.

### Refuse a name defined in both layers

Then a personal catalog could not contain any command a project also defines,
and the failure would appear in whichever workspace the person happened to open.
Overriding with a visible mark is the behaviour that keeps both files editable.

### Copy user commands into each workspace

They drift immediately, and the project's copy is then something the team has to
review and maintain. The point of the layer is that it stays personal.

### An environment variable instead of a flag and a config field

An env var cannot be set per invocation in a way a person can read off the
command line, and this install already layers options through the config file.
The flag and the field follow the existing path; the config directory itself
still follows the environment where that is the convention.

## Verification

`go test ./commands/` — `TestLoadLayersLetsTheWorkspaceShadowAUserCommand`,
`TestLoadLayersTreatsMissingDirectoriesAsEmpty`,
`TestLoadLayersNamesTheLayerInErrors`. `go test ./cli/` —
`TestUserLevelCommandIsAvailableInAWorkspaceWithoutCommands`,
`TestWorkspaceCommandShadowsTheUserCommandOfTheSameName`,
`TestCommandListingShowsTheSourceLayer`,
`TestMalformedUserCommandNamesTheUserDirectory`,
`TestMissingUserCommandsDirectoryIsNotAnError`,
`TestUserCommandsFlagOverridesTheDefaultUserDirectory`,
`TestDefaultUserCommandsDirectoryFollowsTheConfigDirectory`,
`TestUserCommandsDirectoryComesFromTheConfigFile`, and the existing listing and
unknown-command tests, which now show the layer. Plus `go test ./cli/ -count=1`,
`go test -race ./cli/`, `go test ./... -count=1`, `go vet ./...`, `gofmt -l`, and
`go test ./docs/...`.
