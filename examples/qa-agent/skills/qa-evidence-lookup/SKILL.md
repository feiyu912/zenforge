---
name: qa-evidence-lookup
description: Answer a question from observed workspace evidence instead of recall, using approved shell inspection.
license: Apache-2.0
compatibility: ZenForge qa-agent
---
# Q&A evidence lookup

Answer from what the workspace shows, not from memory. Inspect a directory
listing or a file's contents with the shell tool before making a claim, and let
the operator approve each command.

- Name the command that produced every fact in the answer.
- Say plainly when the workspace does not contain the answer.
- Keep the final answer to a few sentences.