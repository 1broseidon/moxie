# CLI Reference

## Commands

### `moxie init`

Interactive setup for Telegram. Prompts for bot token, chat ID, and a default workspace, writes `~/.config/moxie/config.json`, and can optionally install/start the background service. This is the recommended first-run path.

For Slack and Webex, configure `~/.config/moxie/config.json` manually for now.

### `moxie serve`

Start the chat agent service. Runs all configured transports and dispatches messages to agent backends.

```bash
moxie serve [--cwd <dir>] [--transport <telegram|slack|webex>]
```

| Flag | Description |
|------|-------------|
| `--cwd` | Explicit working directory override. Without this flag, `moxie serve` prefers the current shell directory, then `default_cwd`, then the platform workspace default. |
| `--transport` | Run only the specified transport |

### `moxie send`

Send a message directly to the configured transport. If multiple transports are configured, use `--transport telegram`, `--transport slack`, or `--transport webex`.

```bash
moxie send [--transport <telegram|slack|webex>] <message>
```

For Webex, this uses `channels.webex.channel_id`, which must be a **1:1 direct room ID**.

### `moxie messages`

List recent messages. `moxie msg` is an alias.

```bash
moxie messages [--json|--raw] [-n N]
```

### `moxie schedule`

Manage scheduled messages and dispatches.

Supported schedules are materialized automatically into native backends on macOS (`launchd`) and Windows (Task Scheduler) when possible. Unsupported shapes fall back to Moxie's in-process scheduler. Linux currently uses the in-process scheduler only.

```bash
moxie schedule add [flags]      # Create a schedule
moxie schedule list             # List all schedules
moxie schedule show <id>        # Show schedule details
moxie schedule rm <id>          # Delete a schedule
```

Use `--conversation <provider:channel[:thread]>` to target a specific conversation directly, or `--transport <telegram|slack|webex>` to use that transport's configured default conversation. If only one transport is configured, `--transport` can be omitted.

#### `schedule add` flags

| Flag | Description |
|------|-------------|
| `--transport` | Use the configured default conversation for one transport |
| `--conversation` | Target a specific conversation ID directly |
| `--action` | Required: `send` or `dispatch` |
| `--in` | Relative one-shot delay (e.g. `5m`, `2h`) |
| `--at` | Exact one-shot time. Accepts RFC 3339, `YYYY-MM-DDTHH:MM`, or `YYYY-MM-DD HH:MM` |
| `--every` | Recurring elapsed-time interval (e.g. `15m`, `2h`) |
| `--cron` | Recurring portable 5-field cron expression |
| `--text` | Required message or prompt text |
| `--backend` | Override backend for `dispatch` schedules |
| `--model` | Override model for `dispatch` schedules |
| `--thread` | Override thread for `dispatch` schedules |
| `--cwd` | Override working directory for `dispatch` schedules |

Use exactly one of `--in`, `--at`, `--every`, or `--cron`.

For `dispatch` schedules, Moxie captures the current backend, model, thread, and working directory at creation time unless you override them explicitly. `send` schedules ignore those dispatch-specific overrides.

`moxie schedule show <id>` includes sync metadata such as `Managed by`, `Sync state`, and `Sync error` when applicable, which is how you can see whether a schedule is running natively or via the in-process fallback.

### `moxie subagent`

Delegate work to a background agent.

```bash
moxie subagent --backend <name> --text <task> [flags]
moxie subagent list [--all]     # List active subagent jobs
moxie subagent show <job-id>    # Show full details for a job
moxie subagent cancel <job-id>  # Cancel a running job
```

| Flag | Description |
|------|-------------|
| `--backend` | Required. Target backend |
| `--text` | Required. Task prompt |
| `--context-budget` | Context budget for compiled parent context |
| `--model` | Model override |
| `--cwd` | Working directory override |
| `--parent-job` | Explicit parent dispatch job to attach to |

`moxie subagent list` shows active subagent jobs by default. Use `--all` to include completed and canceled jobs. `moxie subagent show` displays full job details including status, backend, model, thread, depth, attempt, run ID, and timestamps.

### `moxie workflow`

Run and manage bounded parallel workflows. The shipped MVP supports the `fanout` strategy, which launches the worker specs listed in `--workers` in parallel and runs one merge step to combine their results.

**Workflows are quiet by default.** No progress updates or intermediate worker output are delivered during a run. Only the final merged result is sent. Use `moxie workflow watch` to observe live output.

```bash
moxie workflow run fanout --workers <backend[:model][,backend[:model]...]> --merge <backend[:model]> --text <task>
moxie workflow list                     # List active workflows (use --all to include terminal workflows)
moxie workflow show <workflow-id>       # Show full details (status, workers, merge job)
moxie workflow watch <workflow-id>      # Stream workflow events until completion
moxie workflow cancel <workflow-id>     # Mark the workflow and child jobs canceled
```

#### `workflow run fanout` flags

| Flag | Description |
|------|-------------|
| `--workers` | Required. Comma-separated worker specs in `backend[:model]` form |
| `--merge` | Required. Merge-step agent in `backend[:model]` form |
| `--text` | Required. Task prompt sent to the workflow |
| `--notify` | Optional notification mode. Defaults to `silent` |

Use `fanout` for independent parallel subtasks where a single merge step can combine the outputs. `moxie workflow cancel` marks the workflow and child jobs as canceled; it does not promise to interrupt an already-running worker process immediately. For sequential or interdependent work, use `moxie subagent` instead.

### `moxie result`

Retrieve subagent results.

```bash
moxie result list [--limit <n>] # List completed results
moxie result show <id>          # Show a specific result artifact
moxie result search <query>     # Search result artifacts by task text
```

### `moxie threads`

View thread history.

```bash
moxie threads show <id>         # Show turns for a thread
```

### `moxie service`

Control the background service.

```bash
moxie service install [--cwd <dir>] [--transport <telegram|slack|webex>]
moxie service uninstall
moxie service start
moxie service stop
moxie service restart
moxie service reload
moxie service status
```

`moxie service install` uses `--cwd` if provided. Otherwise it uses `default_cwd` from config, or the platform-managed workspace default.

On Linux, `reload` sends `SIGHUP` to the running service so it can reload config and backend definitions without exiting the process.

On macOS, `moxie service` manages the LaunchAgent `io.github.1broseidon.moxie` at `~/Library/LaunchAgents/io.github.1broseidon.moxie.plist`.

On Windows, `moxie service install` and the related service control commands are not implemented yet.

## Operator Commands

These commands are mainly for Telegram transport troubleshooting, scripted intake tests, and native schedule plumbing. Most users should not need them during normal use.

### `moxie schedule fire`

Run a schedule immediately by ID.

```bash
moxie schedule fire <id>
```

This is primarily internal/operator plumbing used by native `launchd` and Task Scheduler entries. Normal schedule management should go through `moxie schedule add`, `list`, `show`, and `rm`.

### `moxie poll`

Show only new Telegram messages since the last poll and advance the stored update cursor.

```bash
moxie poll [--json|--raw]
```

### `moxie cursor`

Inspect or modify the stored Telegram update cursor.

```bash
moxie cursor                    # Show current position
moxie cursor set <update_id>    # Set to specific position
moxie cursor reset              # Reset to 0
```
