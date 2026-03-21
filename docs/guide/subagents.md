# Subagents

Delegate focused work to a different backend while the primary conversation continues on its own backend.

## How it works

1. The primary agent (or you via CLI) creates a subagent job targeting a different backend
2. Moxie dispatches the task to the target backend in the background
3. When the subagent completes, the result is synthesized back — a new dispatch to the primary backend with the subagent's output as context
4. The primary agent delivers the final response to your chat

This lets you combine strengths: use Claude for reasoning and Codex for sandboxed code execution, for example.

## CLI usage

```bash
moxie subagent \
  --backend codex \
  --text "Write unit tests for internal/auth/handler.go"
```

| Flag | Description |
|------|-------------|
| `--backend` | Required. Target backend for the delegated task |
| `--text` | Required. The task prompt |
| `--context-budget` | Optional. Context budget for compiled parent context |
| `--model` | Optional. Model override for the target backend |
| `--cwd` | Optional. Working directory override |
| `--parent-job` | Optional. Explicit parent dispatch job to attach to |

## Agent-initiated delegation

The primary agent can also delegate work by calling `moxie subagent` as a shell command during its conversation. The agent has access to `moxie subagent --help` which documents the flags and behavioral guidance.

## Retrieving results

```bash
moxie result list [--limit <n>]  # List completed subagent results
moxie result show <id>         # Show a specific result artifact
moxie result search <query>    # Search artifacts by task text
```

## Depth limits

Subagents can themselves create subagents, but this is capped at a configurable depth to prevent runaway recursion. The default is 3 levels.

```json
{
  "subagent_max_depth": 3
}
```
