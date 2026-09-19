# ADR 0102: The Console's Settings And Credential Persist In A Host-Owned File

Status: accepted

Amends [ADR 0084](0084-one-credential-that-stays-write-only.md) (the credential was
never on disk, because there was no document to write it into),
[ADR 0087](0087-the-settings-panel-is-served-with-a-real-schema.md) (its revision and
fencing rules now have a durable home),
[ADR 0094](0094-the-host-holds-the-consoles-own-settings-namespaces.md) (its
"A host restart shows the notice again" consequence stops holding), and
[ADR 0101](0101-the-console-workspace-registry-is-process-local.md) (which cited the
document-less host as its reason). It does not add a remote method: the console was
already writing all of this, and the writes now outlive the process that took them.

## Context

`zenforge serve` answers the console's settings namespaces from the running process.
An operator who sets the endpoint, picks a model and pastes a key in the browser gets a
Models page that reflects it immediately -- and a host that, on the next start, reports
`hasDocument: false`, resets every namespace's revision to its initial value, and asks
for the key again. The handover recorded the same fact as the operator's daily
inconvenience: the host at `127.0.0.1:8787` needs its credential re-typed once per
restart.

The pieces were all in place except the durable one. Every console write already funnels
through `SettingsDocumentStore` (`internal/dshapi/settings.go`): the endpoint, the
model, the key, the console-owned namespaces, and -- through the sibling stores built in
`cli/serve.go` -- the hand-declared provider profiles (ADR 0095). ADR 0087 had already
made the revision a real number that fences writes, and ADR 0084 had made the key
write-only on the wire. What was missing was a file, and a file that holds a credential
has exactly one job that is harder than storing bytes: deciding where it is allowed to
be.

Two shapes were on the table and one was declined with the operator: keep the settings
on disk and the credential only in an environment variable, or persist both. Persisting
both was chosen, because the environment-variable shape leaves the operator's actual
gesture -- pasting a key into the console -- with nowhere to go.

## Decision

### One document, in the host's own configuration directory, and nowhere it would leak

`console-settings.json` lives in the directory the CLI's user config layer already uses
(`configlayer.UserConfigDir()`: `ZENFORGE_CONFIG_DIR`, else `$XDG_CONFIG_HOME/zenforge`,
else `~/.config/zenforge`), which is also where the user's `zenforge.json` is read from.
`--settings-file` overrides the path verbatim. Two placements are refused:

- **Inside the served workspace.** `workspaceFiles/read` is confined to the workspace
  root (ADR 0089), and a document inside it is a file the console can hand back to a
  browser. A relative `--settings-file console-settings.json` is exactly that case,
  because the workspace defaults to the working directory -- which, in practice, is the
  repository. The refusal names the path, the workspace and the flag.
- **Anywhere the host cannot find a configuration directory at all.** With no home and no
  `XDG_CONFIG_HOME`, this host says once, at startup, that the console's settings will
  not survive a restart, and keeps them in memory. It does not pick a surprising place
  to put a secret.

The document is not a `configlayer` layer, and `zenforge run` does not read it. It is
adapter state written by the console adapter, and the framework's own configuration
composition is untouched by it (ADR 0099).

### One file for the settings and the credential

A separate credential file would need two commits to make one console write durable and
could be caught between them -- a profile whose key had not arrived. One file lets one
atomic write cover the whole state: the live endpoint, model and inline credential, the
namespaces the console owns, the declared provider profiles, and the revision each
namespace stands at.

### `0600`, staged and renamed, and a mode it will not load otherwise

The bytes go to a staging file in the same directory, created at `0600`, flushed, then
renamed onto the document's name; the directory is created `0700`. A reader therefore
sees either the previous document or the new one, never a half-written file, and the
credential is never briefly readable by others under a permissive umask.

On the way in the check runs the other direction: a document whose mode grants access to
group or others is **refused at startup**, naming the file, its mode and `chmod 600` as
the remedy. This host does not silently rewrite permissions on a file it did not create.

### A document that cannot be read stops the host, and never quotes itself

A missing file is a normal start. A file that is present and unreadable -- truncated,
not JSON, a field of the wrong type, a key this host does not store, a version from a
newer host -- is refused before a listener opens, with a message that names the file and
the kind of damage. Starting empty instead would show an operator a Models page that has
lost their endpoint, their profiles and their key, which is the silent degradation this
tier refuses everywhere else.

The message is deliberately free of the file's contents. The standard decoder quotes the
value it could not decode, and the value it is failing on may be precisely the
credential, so a type error contributes the field it landed on and nothing more.

### The document names only what the console moved, and says what it overrode

The document records a settings field only where the live state moved off the startup
seed, and leaves the field out otherwise. That omission is load-bearing: the store holds
the whole configuration and writes a snapshot, so a console write that touched only the
model would otherwise name the credential too -- as the empty string, because this host
starts without one -- and the next start would read that as "the console cleared my key"
and wipe a key the operator supplies with `--api-key` or keeps in the environment named
by `--api-key-env`. Naming a field is how the document says the console spoke about it;
silence is not the same as a statement, and the four settings fields are pointers so
"the console cleared this" and "nobody ever said anything" stay different facts. A
credential the console actually clears differs from a seed that carried one, so the
clear is recorded and stays recorded.

On a start where a document exists, every field it names replaces the seed's, and the
host logs which fields it overrode -- `baseUrl`, `model`, `provider`, `api-key` -- by
name and never by value. The rule is the one that makes the feature worth having: a
console write is the operator's most recent intent, and a shell that repeats an older
flag on every start must not silently undo it. A field the console never moved is not
named, so the flag keeps answering it and the startup line stays quiet about it: a log
that claimed an override where none happened would train the operator to ignore the one
that is real.

`hasDocument` reports what is now true, and the revisions come out of the file rather
than starting again. A console tab held open across a restart fences its next write
against the number the document carries, and a stale number is refused as
`settings/conflict` exactly as ADR 0087 requires.

### The document is the commit point, and a write that cannot land is refused

A settings change commits in memory, then the document is written. A write that fails --
a disk that will not take it, a path that stopped being a directory -- puts the store
back where it was and returns the failure, so the running process and its file never
disagree and the console is never told a setting is saved when the file it would be
restored from does not hold it. The one exception is the revision number, which the
handler advances after the change it numbers has already committed: a document that will
not take the number gets the number put back and a log line, which leaves the value and
the file agreeing and errs in the direction that refuses a stale write rather than
allowing one.

## Consequences

- A restart costs the operator nothing. The endpoint, the model, the credential, the
  declared provider profiles and the welcome-notice acknowledgement are all read back at
  startup, and the first model catalog the host serves comes from the document rather
  than from the command line.
- The two `docs/limitations.md` entries that said the revision and the acknowledgement are
  process-local are replaced. What is true instead: the document is not watched, so an
  edit made to it by hand while the host runs is overwritten by the next commit (last
  writer wins, and one host's write is a whole-state snapshot), and two `serve` processes
  sharing one configuration directory replace each other's document rather than merging.
- `settings/openSettingsDocument` stays refused by name. This host has a document and
  still has no native editor to open it in; the path is on the startup log, which is the
  host's own channel.
- The credential keeps every rule ADR 0084 gave it. It crosses the wire in one direction
  only, appears in no reply, no log line, no event, and no error message, and the single
  file written at `0600` is now the one durable place it exists.
- The console's workspace registry is still process-local (ADR 0101), and a per-session
  model selection is still this process's. The registry is the next candidate for this
  document; it is deliberately not in this change, because a registered directory the
  host cannot actually run a session in is a different question from a key.

## Alternatives Rejected

### Settings on disk, credential only through the environment

Offered and declined. It leaves the console's own key field with nowhere to persist,
which is the gesture the operator actually performs.

### Writing into `~/.config/zenforge/zenforge.json`

That file is hand-authored framework configuration, composed by `configlayer` from
ordered layers, and it has no write-back path. Mixing adapter state into it would make
`zenforge run` read a console's scratch state as configuration, and would rewrite an
operator's file with generated content.

### `os.UserConfigDir()` (`~/Library/Application Support/zenforge`)

A second configuration home, on a machine that already has one. The CLI's user layer,
`ZENFORGE_CONFIG_DIR` and the documented table in `docs/config-reference.md` all name
`~/.config/zenforge`; a host that keeps its settings somewhere else teaches the operator
two places to look and one of them to forget.

### Repairing a lax mode on read

A file this host did not create, opened for reading, and then `chmod`ed: silently taking
control of an operator's permissions is worse than refusing and telling them the command
that fixes it.

### A settings table in the run/event store

The event log is durable state for runs, with its own schema versioning, and it is
readable through the console's session routes. A credential does not belong in either.

## Verification

`go test ./cli/ -run TestConsoleSettingsDocument` -- the round trip across a restart
(endpoint, model, credential, acknowledgement, declared profile and revision all read
back), the `0600` mode with no staging file left behind, the refusal of a document inside
the served workspace, the default path in the host's configuration directory and never
inside the checkout, the loud process-local fallback when there is no configuration
directory, six damaged documents refused by name with the credential absent from every
message, a failed write leaving the old value standing, the startup line that names the
fields it overrode without naming their values, a field the console never moved staying
out of the file so a host configured with `--api-key` keeps its key across a write that
only touched the model, a cleared override staying cleared, and the key appearing in
exactly one file.

`go test ./internal/dshapi/ -run TestSettings` -- `hasDocument` answered from the store in
both directions, and a store that versions its own namespaces asked instead of the handler
counting: the loaded revision fences a write, an accepted write moves it, the superseded
number is refused as `settings/conflict`, and the handler's own map stays untouched.

Live, against a host built from this checkout:

```sh
curl -s -X POST http://127.0.0.1:8787/api/settings/describe \
  -H 'content-type: application/json' \
  -d '{"type":"client-request","rpcId":"d1","method":"settings/describe","payload":{"args":{}}}'
# hasDocument is true, and the namespace revisions are the numbers the file carries.
# Take one, mutate with it as expectedRevision, restart the host, describe again: the
# profiles, the endpoint and the credential are still there, and the revision did not
# start over.
```

Run against the operator's host, which serves the repository itself as its workspace:
the write fenced at the revision `describe` reported was accepted and moved it by one,
the superseded revision was refused as `settings/conflict`, and the document appeared at
`~/.config/zenforge/console-settings.json`, mode `0600`, outside the workspace the page
can browse -- which is the placement rule doing its job on the only host that matters.
