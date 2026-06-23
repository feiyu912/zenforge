# Provider Guide

ZenForge keeps provider protocol handling inside model adapters. The harness
uses the provider-neutral `model.Model` interface.

## OpenAI-Compatible

```go
model := openai.New(openai.Config{
    APIKey: os.Getenv("OPENAI_API_KEY"),
    Model:  "gpt-4.1",
})
```

The OpenAI-compatible adapter uses Chat Completions streaming and function
tools. Providers that expose an OpenAI-compatible Chat Completions API should
use this adapter with their own `BaseURL`; they do not need a new ZenForge
provider type.

CLI config:

```json
{
  "model": {
    "provider": "openai",
    "name": "gpt-4.1",
    "apiKeyEnv": "OPENAI_API_KEY"
  }
}
```

OpenAI-compatible vendor example:

```json
{
  "model": {
    "provider": "openai",
    "name": "MiniMax-M3",
    "apiKeyEnv": "MINIMAX_API_KEY",
    "baseUrl": "https://api.minimax.io/v1"
  }
}
```

```bash
export MINIMAX_API_KEY=...
zenforge run --provider openai --model MiniMax-M3 --api-key-env MINIMAX_API_KEY --base-url https://api.minimax.io/v1 "Analyze this repo"
```

## Anthropic

```go
model := anthropic.New(anthropic.Config{
    APIKey: os.Getenv("ANTHROPIC_API_KEY"),
    Model:  "claude-model",
})
```

The Anthropic adapter uses the Messages API, maps ZenForge system messages to
the top-level `system` field, and maps ZenForge tool calls to Anthropic
`tool_use`/`tool_result` content blocks.

CLI config:

```json
{
  "model": {
    "provider": "anthropic",
    "name": "claude-model",
    "apiKeyEnv": "ANTHROPIC_API_KEY"
  }
}
```

The CLI also accepts:

```bash
zenforge run --provider anthropic --model claude-model --api-key-env ANTHROPIC_API_KEY "Analyze this repo"
```

MiniMax can also be used through its Anthropic-compatible endpoint, matching the
DeepAgents-style environment:

```json
{
  "model": {
    "provider": "anthropic",
    "name": "MiniMax-M3",
    "apiKeyEnv": "ANTHROPIC_API_KEY",
    "baseUrl": "https://api.minimaxi.com/anthropic/v1"
  }
}
```

```bash
export ANTHROPIC_API_KEY=...
export ANTHROPIC_BASE_URL=https://api.minimaxi.com/anthropic/v1
zenforge run --provider anthropic --model MiniMax-M3 --api-key-env ANTHROPIC_API_KEY --base-url "$ANTHROPIC_BASE_URL" "Analyze this repo"
```

## Boundary

Provider adapters own API-specific request/stream parsing. The harness should
not parse provider chunks directly. SDK users can bypass CLI providers entirely
by constructing their own `model.Model` implementation and passing it to
`zenforge.New`; this is the preferred extension point for custom gateways,
test doubles, or application-owned model clients.
