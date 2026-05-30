# Memory Adapter Guide

ZenForge keeps memory outside the core harness. Host platforms retrieve memory
from their own stores, then pass normalized context into ZenForge.

`adapters/memory` provides a small bridge for that boundary.

## Adapt Memory Into A Task

```go
store := memory.NewStaticStore(
    memory.Entry{ID: "profile", Text: "The user prefers concise answers.", Score: 0.9},
)

augmenter := memory.Augmenter{
    Store:      store,
    MaxEntries: 5,
}

task, entries, err := augmenter.AugmentTask(ctx, zenforge.Task{
    RunID: "run_123",
    Input: "Plan the next refactor step.",
})
if err != nil {
    return err
}

events, err := agent.Stream(ctx, task)
```

The augmented task input starts with a `Relevant memory` block, followed by the
original user request. The selected entries are also copied into `task.Meta`
under `memory.entries` so platform adapters can audit what was injected.

## Store Interface

```go
type Store interface {
    Search(ctx context.Context, query memory.Query) ([]memory.Entry, error)
}
```

Platforms can implement this interface using their existing memory systems:
chat summaries, profile facts, vector search, workspace notes, or catalog
metadata.

## Boundary

Memory retrieval can involve tenancy, privacy, ranking, summarization, and
retention policy. Those concerns remain outside ZenForge core.

The adapter only answers:

- which entries were selected;
- how they are formatted into a normalized task;
- how the selected entries are exposed in metadata.

## Safety Notes

Memory can contain stale, private, or prompt-injection-like text. Hosts should:

- retrieve only entries for the current tenant/session;
- cap entries and text length before augmentation;
- redact secrets before tracing or persisting prompts;
- make injected memory visible in audit metadata.
