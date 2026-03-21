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

Send a message directly (Telegram only).

```bash
moxie send <message>
```

### `moxie messages`

List recent messages.

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

### `moxie subagent`

Delegate work to a background agent.

```bash
moxie subagent --backend <name> --text <task> [flags]
```

| Flag | Description |
|------|-------------|
| `--backend` | Required. Target backend |
| `--text` | Required. Task prompt |
| `--model` | Model override |
| `--cwd` | Working directory |
| `--thread` | Thread ID |

### `moxie result`

Retrieve subagent results.

```bash
moxie result list               # List completed results
moxie result show <id>          # Show a specific result
```

### `moxie threads`

View thread history.

```bash
moxie threads show <id>         # Show turns for a thread
```
