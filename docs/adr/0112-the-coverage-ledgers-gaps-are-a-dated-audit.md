# ADR 0112: The Coverage Ledger's Gaps Are a Dated Audit

Status: accepted

Extends [ADR 0099](0099-the-framework-core-and-the-console-adapter-are-separate-layers.md)
(the ledger as the console tier's honest state) and records the console tier's own bookkeeping
the way [ADR 0109](0109-the-console-host-keeps-its-run-registry-on-disk.md) did for the run
registry.

## Context

The ledger's method tables have been enforced from the day they were written: a test reads
`docs/dsh-console-coverage.md`, asks the host's own dispatcher about every row, and fails when
a row's state and the routing table disagree. The prose was not, and it drifted:

```
## Next up
1. **`ui-directory-picker-browse` is withheld, so the workspace picker has no add action at
   all.** ... Re-testing it is the next step, on a scratch port ...
```

Commit `a32cc8c` ("Load the browse directory picker instead of withholding it") had already
re-tested that activation, moved the plugin out of the roster's `blocked` list, and pinned it
with a test. The list still sent the next window to redo the re-test. Checked live on the
operator's host before rewriting it:

```
$ curl -s http://127.0.0.1:8787/          # window.__DSH_BOOT__, 53 entries
entries include @deepseek-ai/dsh-client-ui-directory-picker-browse, and not the native sibling
$ curl -s -o /dev/null -w '%{http_code} %{size_download}\n' \
    'http://127.0.0.1:8787/plugins/??@deepseek-ai/dsh-client-ui-directory-picker-browse/client.js'
200 49199
$ python3 -c 'import json; r=json.load(open("internal/dshmount/roster.json")); print([b["dir"] for b in r["blocked"]])'
['client/ui-directory-picker-native', 'extensions/ui-cordis']
```

Every advertised bundle in the graph answered 200, and the only `inject` names absent from the
graph are the shell-provided singletons and one deliberately omitted package — the console's
documented degradation path. The gap was in the document, not the host.

The failure mode is specific to prose: a table cell is machine-checked, a sentence is not, and
a sentence that claims a *capability is missing* is the one a reader acts on.

## Decision

The ledger's "Next up" list is a dated audit, and its claims are kept where a test can hold
them:

- the list is stamped with the date it was derived and the sources it was derived from — the
  routing table in `internal/dshapi/handler.go`, the plugin roster in
  `internal/dshmount/roster.json`, and a live host's boot graph;
- an item that has shipped is **removed** from the list; a shipped item is never left as
  history, because the list is read as work remaining;
- what is served, refused, streamed or unserved stays in the table, which the existing test
  pins against the dispatcher;
- which console plugins are served, withheld or omitted is the **roster's** answer, never the
  ledger's: the ledger may point at the roster, not restate it;
- a test rejects the two ways the list went stale: no next-up item may name a method the host
  routes, and no next-up item may name a plugin the roster serves.

## Consequences

- A window that reads the ledger is told what is actually left: `session/openWorkspacePath`
  and `session/canOpenWorkspacePath` head the list, the directory-picker family no longer
  appears as a gap, and the refused native half is named where it belongs (ADR 0100 and the
  roster).
- The prose can still be wrong in ways no test sees — a gap that is neither a method nor a
  plugin, such as "this panel renders nothing". Those are operator reports and belong in
  `docs/limitations.md`, which is where a chain found while verifying puts its finding.
- Re-deriving the list is a few commands, so a window can afford to do it before trusting it;
  the ADR names them rather than leaving the next reader to guess.