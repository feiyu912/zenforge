# ADR 0012: Model Adapters Own Provider Protocol

Status: accepted

## Context

Provider streaming protocols differ. OpenAI-compatible, Anthropic-compatible,
Ollama, and local models all expose slightly different formats, especially for
tool-call streaming.

## Decision

Model adapters own provider-specific protocol parsing.

The harness receives normalized model events:

- text delta;
- final assistant message;
- tool call specs;
- usage;
- error.

The harness does not parse OpenAI or Anthropic chunks directly.

## Consequences

Benefits:

- harness loop remains provider-agnostic;
- model adapters can be tested independently;
- new providers do not affect core loop.

Costs:

- model adapter interface must be expressive enough for tool calls;
- streaming tool-call accumulation lives in adapters.
