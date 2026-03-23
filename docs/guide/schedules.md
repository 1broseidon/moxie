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
| `--in` | Relative one-shot delay from now | `--in 5m`, `--in 2h` |
| `--at` | Absolute one-shot time (RFC 3339) | `--at 2026-03-20T10:00:00-05:00` |
| `--every` | Recurring elapsed-time interval | `--every 15m`, `--every 2h` |
| `--cron` | Recurring cron expression | `--cron "0 1 * * *"` |

Moxie keeps one portable schedule model across platforms and now materializes supported schedules into native `launchd` jobs on macOS automatically. No separate install or sync step is required after `moxie schedule add`.

Current platform behavior:

- macOS: supported one-shot, interval, and portable calendar schedules are installed as per-user `launchd` jobs
- macOS fallback: second-precision one-shots, far-future one-shots that need explicit year precision, and calendar shapes that do not map cleanly to `launchd` stay on the in-process scheduler automatically, and the fallback reason is recorded in schedule sync metadata
- Linux: native timer integration is not implemented yet
- Windows: schedules are not yet backed by Task Scheduler

Linux and Windows still rely on Moxie's in-process scheduler, so on those platforms you must keep `moxie serve` running if you want schedules to keep firing.

Use `--in` for a one-time relative reminder and `--every` for a repeating elapsed-time interval.

## Portable cron subset

`--cron` accepts the common portable 5-field form:

```text
minute hour day-of-month month day-of-week
```

Supported portable forms:
- exact values like `0 9 * * 1`
- wildcards like `*`
- safe lists and ranges like `1,3,5` or `MON-FRI`
- named months/days such as `JAN` or `MON`
- safe descriptors that normalize into the portable form, such as `@daily`

Notably rejected:
- step expressions like `*/5`
- specials like `?`, `L`, `W`, `#`, and `@reboot`
- cron expressions that restrict both day-of-month and day-of-week at the same time

Moxie preserves the original cron string for display, while normalizing it internally into the canonical calendar fields used by the scheduler.

## Examples

### Reminder in 5 minutes

```bash
moxie schedule add \
  --transport telegram \
  --action send \
  --in 5m \
  --text "Call John"
```

### Queue check every 30 minutes

```bash
moxie schedule add \
  --transport telegram \
  --action dispatch \
  --every 30m \
  --text "Check queue depth and summarize any backlog"
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
moxie schedule fire <id>      # Internal/operator trigger path used by native backends
```

`moxie schedule fire <id>` is primarily internal plumbing for operator use and future native scheduler backends. Normal schedule creation and execution still happen through `moxie schedule add ...` plus the existing runtime path.

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
