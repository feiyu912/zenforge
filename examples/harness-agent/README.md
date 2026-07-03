# Harness agent

This is a small external-application-style agent that uses ZenForge's public
provider factory, Agent Skills, typed tools, CLI approval broker, and built-in
Docker sandbox. The current directory is mounted read-only at `/workspace`.
The bundled `skills/project-orientation/SKILL.md` is a real instruction package;
`inspect_path` is a separate ordinary typed tool.

Set the provider protocol and matching endpoint credentials:

```sh
export ZENFORGE_PROVIDER=openai # or anthropic
export ZENFORGE_MODEL=your-model
export ZENFORGE_API_KEY=your-key
export ZENFORGE_BASE_URL=https://your-endpoint.example/v1

go run ./examples/harness-agent -question "What kind of project is this?"
```

The default skill root is `examples/harness-agent/skills`. Override it with
`-skill-root /path/to/skills` or `ZENFORGE_SKILL_ROOT=/path/to/skills`. The
root may be one skill directory or a directory of skill subdirectories.

Without `-question`, the first stdin line is the question. Later stdin lines
remain available for CLI approval choices.

MiniMax is not a separate ZenForge protocol. Use it through the Anthropic- or
OpenAI-compatible BaseURL advertised by the selected MiniMax endpoint. The API
key must match the selected protocol and endpoint; an Anthropic-compatible key
cannot be assumed to work with an OpenAI-compatible URL, or vice versa.
