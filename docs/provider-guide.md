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
tools.

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

## Boundary

Provider adapters own API-specific request/stream parsing. The harness should
not parse provider chunks directly.
