# ADR 0032: Layered Configuration, Managed Requirements, And Redacted Secrets

Status: accepted

## Context

ZenForge's CLI read exactly one JSON file (`--config`, or nothing) and
applied flags on top. Codex instead composes a *stack* of configuration
layers — packaged defaults, MDM, system, enterprise-managed, user (with
an optional profile), project, and session flags — where each layer
keeps its source, higher precedence overrides lower per leaf key, and a
managed `requirements.toml` can validate values (`allowed` sets) or
impose them (`enforce`). Codex also has a `strict-config` mode that
turns unrecognized fields into startup errors, and a `RedactedString`
whose `Debug` output never prints the secret.

ZenForge had none of this: no ambient or repository-local configuration,
no profiles, no administrator-enforced policy, no unknown-field
detection, and no type for secrets.

## Decision

### Precedence follows codex's table, one explicit `--config` layer added

`configlayer.Kind.Precedence` mirrors codex's `ConfigLayerSource`:
system 10, user 20, profile 21, project 25, session flags 30. ZenForge
inserts the explicit `--config` file at 27 — above the discovered project
layer, below flags — because `--config` is the "use exactly this file"
switch and must still lose to an explicit flag.

Merging is a deep object merge with per-leaf provenance
(`Stack.Merge` returns both the document and a `path → Source` map).
Objects merge key by key, every other value replaces, and a layer that
makes an existing key a different *shape* (object versus scalar) is an
error rather than a silent clobber: a config that says `agent` is an
object in one layer and a string in another is a mistake worth stopping
for.

Ambient layers (`/etc/zenforge/zenforge.json`, the user file, and
`.zenforge/zenforge.json` discovered by walking up from the workspace)
participate by default, matching codex; `--ignore-user-config` skips the
system and user layers, the same escape hatch codex's `exec` exposes.

### Profiles sit directly above the layer that defines them

A profile is an ordinary config fragment under `profiles.<name>`. It is
extracted from the merged document and re-added with a rank of
`defining.Precedence() + 1`, so a profile declared in the project file
outranks that file's base values but still loses to `--config` and
flags, while a profile declared in the user file behaves exactly like
codex's user-with-profile precedence (21).

An unknown profile is a usage error that lists the available names.
Ignoring `--profile` would run with settings the caller did not intend,
which is worse than refusing to start. `WithoutProfiles` strips the
`profiles` key before the document is decoded into the typed schema,
which has no such field.

### Requirements validate and enforce, in that order, after merging

The managed document has two sections. `allowed` maps a dotted key to a
permitted set; any merged value outside the set fails with codex's
`ConstraintError` shape — the key, the rejected value rendered as JSON,
the allowed set, and the requirement's source. `enforce` maps a dotted
key to a value that overwrites whatever the layers produced, so an
administrator can pin a policy a user config cannot loosen.

Validation runs before enforcement, so the file states what the operator
*intends* and the resulting value is guaranteed to satisfy it (an
enforced value is checked against the allowed set whenever both name the
same key). An absent key is not a violation: the layers simply did not
set it. The input document is never mutated; `Apply` returns a clone.

An explicitly passed `--requirements` file must exist — a typo in a path
must not silently drop the policy — while the host-wide default path is
optional. A malformed requirements document (empty, non-object
sections, empty allowed lists, null enforced values) fails at parse time.

### Strict mode names the offending layer

`--strict-config` decodes the merged document with
`DisallowUnknownFields` and reports the field together with the layer
that supplied it (`unknown config field "maxStepz" (set by file
(/tmp/typo.json))`). Without the flag, unknown keys are ignored, which
keeps forward compatibility for configurations written against a newer
build.

### Secrets redact on every formatting path, stay transparent in JSON

`redact.String` renders `<redacted>` for `String`, `GoString`, and
`LogValue`, so `%v`, `%s`, `%#v`, and slog never leak; `Reveal` is the
only way to read the value. `MarshalJSON` is deliberately transparent so
a loaded configuration round-trips, and `Describe` reports only `set` or
`unset`.

This is stricter than codex, which redacts only `Debug`: in Go, `%v`
and `%s` route through `Stringer`, and `fmt.Sprintf("%v", config)` is
the accident most likely to reach a log line. Redacting by default is
the safer error, and `Reveal` documents the boundary explicitly.

`model.apiKey` is the inline secret and is applied through
`provider.Config.APIKey`, which already outranks the environment
variable. A missing or empty key still fails with the existing
`<ENV> is not set` diagnostic, whose message names the environment
variable rather than any value.

## Consequences

Benefits:

- operators get a real policy surface (`allowed`/`enforce`) that a user
  or project file cannot override, with errors that name the responsible
  file;
- teams can share profile fragments (`ci`, `review`) instead of juggling
  whole config files, and a typo'd profile fails immediately;
- an unknown or misspelled key can be made fatal on demand, and
  provenance makes the diagnostic actionable;
- a secret in a config file cannot be leaked by an accidental
  `fmt.Sprintf` or structured log call.

Costs:

- configuration now depends on ambient files, so a surprising setting
  can come from outside the repository; `--ignore-user-config`,
  provenance in strict errors, and the documented precedence table are
  the mitigations;
- `redact.String` cannot be used as a plain `string` in arithmetic or
  comparisons without `Reveal`, which is the point but does add
  ceremony;
- JSON-transparent marshaling means `zenforge init`-style dumps *do*
  contain the secret, exactly like codex; the mitigation is
  `apiKeyEnv`/`--api-key-env`, which the reference docs recommend first.

## Alternatives Rejected

### Always Rejecting Unknown Fields

Codex has an explicit `--strict-config` switch for a reason: a config
written for a newer build should not fail an older one by default. Making
strictness opt-in keeps forward compatibility while giving operators the
strict mode when they want it.

### Managed Layer By Precedence Only (No `enforce`)

If the managed layer merely had a very high precedence, a user could
still shadow it by adding a layer above (a later build, a different
entry point). `enforce` is an absolute claim applied after merging,
which is what "administrator policy" has to mean.

### Redacting `MarshalJSON` Too

A redacted serializer makes a config file that was read impossible to
write back, which breaks any tooling that loads, edits, and saves the
file. The secret's threat model is logs and error text, not the file the
operator deliberately wrote it into.

### Requiring Explicit Layer Flags For Everything

Discovery is what makes a repository-local `.zenforge/zenforge.json` and
a per-user profile useful. Codex does discovery with an escape hatch, and
so does this implementation.