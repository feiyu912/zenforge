# ADR 0092: The Console Shows the Product's Name in Its Command Form

Status: accepted

Amends ADR 0079, which put the console under our own name as `ZenForge`.

## Context

ADR 0079 rebranded the console away from the upstream product, and the
browser-visible identity it produced was spelled `ZenForge` -- the sentence-case
stylisation this repository also uses in prose, Go doc comments and MCP tool
descriptions. The console is required to show the product's name as `zenforge`,
which is the form of the name an operator actually uses: it is the binary, the
module and the repository.

Serving a capitalised variant in the title bar is a second spelling of one
product, and the two drift apart the moment either is changed -- the ADR-0079
patch already shows how far a string travels: the manifest, the favicon, the page
title, the boot page wordmark, the locale seats, the layout's title fallback and
the plugins' own copy.

## Decision

### The browser-visible identity is `zenforge`

The page title, the installable-app `name` and `short_name`, the favicon's
accessible name, the brand plugins' `brand.localBuild` strings, the layout's title
fallback and the plugin copy that names the product all read `zenforge`. The
string the build pins as the client title (`DSH_CLIENT_TITLE`) is `zenforge` too,
so the frame title the shell draws and the title the page declares agree.

### Prose keeps the sentence-case stylisation

Go doc comments, documentation, MCP tool descriptions and the `X-ZenForge-*`
headers keep `ZenForge`. Those are sentences and identifiers, not the console's
identity, and lowercasing them would be churn unrelated to what a user sees in
the browser -- and would rename a header that HTTP clients match on.

### The drawn wordmark stays in capitals

The boot page and the conversation hero draw `ZENFORGE`, and the favicon is the
`Z` mark. A wordmark is a drawn graphic rather than a title: it is the same word,
styled. It is recorded here so the distinction is a decision rather than an
oversight.

### Recipe and artifacts carry the same strings

`scripts/build-console.sh` is the recipe a rebuild follows, and the committed
artifacts were updated to match it, so the two cannot disagree about what the
console says. That is also why the change is asserted over the staged bytes rather
than over the recipe.

## Consequences

- The console's title bar, installable-app name, favicon name and brand copy read
  `zenforge`, matching the command an operator types.
- A rebuild reproduces the same identity: the recipe's replacements and its own
  verification greps were moved to the same spelling.
- The capital form is now a defect in a browser-visible byte, so a future patch
  that reintroduces it fails a test instead of shipping quietly.

## Alternatives Rejected

### Keep `ZenForge` in the console

It is what ADR 0079 chose and what the rest of the repository writes. But the
console's identity is the one place the product names itself to a user, and the
name it names itself with is the command form.

### Lowercase the whole repository

Hundreds of doc comments, tool descriptions, example programs and two HTTP headers
would change to no visible benefit, and `X-ZenForge-Signature` would break clients
that match on it.

### Lowercase the drawn wordmark too

The boot page would then disagree with the favicon, which is a glyph and cannot be
either case. A wordmark is styling; the title is identity.

## Verification

`go test ./webui/dsh/ -run TestStagedIdentityIsTheProductsOwnName` -- the served
shell's `<title>zenforge</title>`, no upstream product name in the shell, the
manifest's `name` and `short_name`, the favicon's accessible name, and a walk of
every staged plugin bundle asserting no browser-visible byte spells the brand
`ZenForge`. `go test ./webui/dsh/ ./internal/dshmount/ ./cli/ ./docs/...` covers
the shell the mount serves, the CLI's own title assertion and the documentation
checks.
