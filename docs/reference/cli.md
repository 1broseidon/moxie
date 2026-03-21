# CLI Reference

## Commands

### `moxie init`

Interactive setup for Telegram. Prompts for bot token and chat ID, writes `~/.config/moxie/config.json`.

### `moxie serve`

Start the chat agent service. Runs all configured transports and dispatches messages to agent backends.

```bash
moxie serve [--cwd <dir>] [--transport <telegram|slack>]
```

| Flag | Description |
|------|-------------|
| `--cwd` | Default working directory for agent backends |
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

### `moxie poll`

Show only new messages since the last poll and advance the cursor.

```bash
moxie poll [--json|--raw]
```

### `moxie cursor`

Manage the Telegram update cursor.

```bash
moxie cursor                    # Show current position
moxie cursor set <update_id>    # Set to specific position
moxie cursor reset              # Reset to 0
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
| `--in` | Relative delay (e.g. `5m`, `2h`) |
| `--at` | Absolute time (RFC 3339) |
| `--cron` | Cron expression |
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
moxie result list               # List completed results
moxie result show <id>          # Show a specific result artifact
moxie result search <query>     # Search result artifacts by task text
```

### `moxie threads`

View thread history.

```bash
moxie threads show <id>         # Show turns for a thread
```
