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

## Monitoring subagents

```bash
moxie subagent list [--all]    # Active jobs (--all includes completed/canceled)
moxie subagent show <job-id>   # Full job details: status, backend, thread, etc.
moxie subagent cancel <job-id> # Cancel a running job
```

## Retrieving results

```bash
moxie result list [--limit <n>]  # List completed subagent results
moxie result show <id>         # Show a specific result artifact
moxie result search <query>    # Search artifacts by task text
```

## Bounded parallel work with workflows

For tasks that can be split across multiple independent workers — research sweeps, repo audits, alternative proposals — use `moxie workflow run fanout` instead of multiple subagents. Workflows are **quiet by default**: Moxie sends one acknowledgement when the workflow starts and delivers the merged result when all workers finish. It does not stream per-worker progress into chat unless you request it.

```bash
moxie workflow run fanout \
  --workers claude,claude,pi \
  --merge claude \
  --text "Audit the scheduler and summarize findings"
```

See `moxie workflow --help` and the [CLI reference](../reference/cli.md) for `list`, `show`, `watch`, and `cancel` commands.

Use subagents (not workflows) when the task is sequential or when one result must inform the next step. Use workflows only for bounded parallel work where a merge step can combine independent outputs.

## Sequential tasks

When you ask Moxie to do multiple tasks sequentially (e.g. "do A then B then C"), the synthesis step automatically dispatches the next task after each one completes. The loop continues until all tasks are done or one needs human attention.

## Depth limits

Subagents can themselves create subagents, but this is capped at a configurable depth to prevent runaway recursion. The default is 3 levels.

```json
{
  "subagent_max_depth": 3
}
```

## Resource limits

Moxie enforces limits to prevent runaway fan-out and abuse. All are configurable in `config.json`:

| Limit | Default | Description |
|-------|---------|-------------|
| `max_pending_subagents` | `5` | Max concurrent subagent jobs per conversation |
| `max_jobs_per_minute` | `10` | Rate limit on dispatch and `moxie send` |
| `max_schedule_generation` | `3` | Max schedule→dispatch→schedule recursion cycles |

When a limit is hit, the dispatch fails immediately with a descriptive error rather than queuing silently.

## Preflight checks

Before a subagent job is written, Moxie runs a preflight check against the target backend CLI. If the CLI is missing or unhealthy, the dispatch fails immediately with an actionable error listing available backends. This also applies to jobs entering through any other path (schedule dispatches, recovery after restart) — doomed jobs fail fast instead of retrying three times silently.
