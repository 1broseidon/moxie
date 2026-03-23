# CLI Reference

## Commands

### `moxie init`

Interactive setup for Telegram. Prompts for bot token, chat ID, and a default workspace, writes `~/.config/moxie/config.json`, and can optionally install/start the background service. This is the recommended first-run path.

### `moxie serve`

Start the chat agent service. Runs all configured transports and dispatches messages to agent backends.

```bash
moxie serve [--cwd <dir>] [--transport <telegram|slack>]
```

| Flag | Description |
|------|-------------|
| `--cwd` | Explicit working directory override. Without this flag, `moxie serve` prefers the current shell directory, then `default_cwd`, then the platform workspace default. |
| `--transport` | Run only the specified transport |

### `moxie send`

Send a message directly to the configured transport. If both transports are configured, use `--transport telegram` or `--transport slack`.

```bash
moxie send [--transport <telegram|slack>] <message>
```

### `moxie messages`

List recent messages. `moxie msg` is an alias.

```bash
moxie messages [--json|--raw] [-n N]
```

### `moxie schedule`

Manage scheduled messages and dispatches.

```bash
moxie schedule add [flags]      # Create a schedule
moxie schedule list             # List all schedules
moxie schedule show <id>        # Show schedule details
moxie schedule rm <id>          # Delete a schedule
```

#### `schedule add` flags

| Flag | Description |
|------|-------------|
| `--transport` | Target transport (`telegram` or `slack`) |
| `--conversation` | Target a specific conversation ID |
| `--action` | Required: `send` or `dispatch` |
| `--in` | Relative one-shot delay (e.g. `5m`, `2h`) |
| `--at` | Absolute one-shot time (RFC 3339) |
| `--every` | Recurring elapsed-time interval (e.g. `15m`, `2h`) |
| `--cron` | Recurring cron expression |
| `--text` | Message or prompt text |
| `--backend` | Override backend for `dispatch` schedules |
| `--model` | Override model for `dispatch` schedules |
| `--thread` | Override thread for `dispatch` schedules |
| `--cwd` | Override working directory for `dispatch` schedules |

### `moxie subagent`

Delegate work to a background agent.

```bash
moxie subagent --backend <name> --text <task> [flags]
```

| Flag | Description |
|------|-------------|
| `--backend` | Required. Target backend |
| `--text` | Required. Task prompt |
| `--context-budget` | Context budget for compiled parent context |
| `--model` | Model override |
| `--cwd` | Working directory override |
| `--parent-job` | Explicit parent dispatch job to attach to |

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
moxie service install [--cwd <dir>] [--transport <telegram|slack>]
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

These commands are mainly for Telegram transport troubleshooting and scripted intake tests. Most users should not need them during normal use.

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
