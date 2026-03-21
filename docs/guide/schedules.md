# Schedules

Schedule one-time or recurring messages and agent dispatches.

## Actions

| Action | Description |
|--------|-------------|
| `send` | Deliver a text message to the chat |
| `dispatch` | Run the text as an agent prompt and deliver the result |

## Triggers

Use exactly one trigger per schedule:

| Flag | Description | Example |
|------|-------------|---------|
| `--in` | Relative delay from now | `--in 5m`, `--in 2h` |
| `--at` | Absolute time (RFC 3339) | `--at 2026-03-20T10:00:00-05:00` |
| `--cron` | Recurring cron expression | `--cron "0 1 * * *"` |

## Examples

### Reminder in 5 minutes

```bash
moxie schedule add \
  --transport telegram \
  --action send \
  --in 5m \
  --text "Call John"
```

### Daily security scan at 1am

```bash
moxie schedule add \
  --transport slack \
  --action dispatch \
  --cron "0 1 * * *" \
  --text "Run a security scan on the codebase"
```

### One-shot dispatch at a specific time

```bash
moxie schedule add \
  --transport telegram \
  --action dispatch \
  --at 2026-03-20T10:00:00-05:00 \
  --text "Check the deploy status and report any issues"
```

### Target a specific conversation

```bash
moxie schedule add \
  --conversation slack:C123:1710000000.100 \
  --action send \
  --in 10m \
  --text "Follow up on the PR"
```

## Managing schedules

```bash
moxie schedule list           # List all schedules
moxie schedule show <id>      # Show details for a schedule
moxie schedule rm <id>        # Delete a schedule
```

## Dispatch context

When a schedule uses `--action dispatch`, it captures the current backend, model, thread, and working directory at creation time. You can override these with `--backend`, `--model`, `--thread`, and `--cwd`:

```bash
moxie schedule add \
  --transport telegram \
  --action dispatch \
  --backend codex \
  --model gpt-5 \
  --cwd /home/user/projects/myapp \
  --cron "0 9 * * 1" \
  --text "Weekly code review summary"
```

Without overrides, the dispatch runs against whatever backend the conversation was using when the schedule was created.
