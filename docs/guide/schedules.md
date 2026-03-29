# Schedules

Schedule one-time or recurring messages and agent dispatches.

Moxie keeps one portable schedule model across platforms. After `moxie schedule add`, supported schedules are materialized automatically into native backends on macOS (`launchd`) and Windows (Task Scheduler). Unsupported shapes fall back to Moxie's in-process scheduler, and the fallback reason is recorded in schedule sync metadata.

## Targeting a schedule

Target a schedule in one of two ways:

- `--conversation <provider:channel[:thread]>` to schedule directly into a specific conversation
- `--transport <telegram|slack|webex>` to use that transport's configured default conversation

If only one transport is configured, `--transport` can be omitted.

## Actions

| Action | Description |
|--------|-------------|
| `send` | Deliver a text message to the chat |
| `dispatch` | Run the text as an agent prompt and deliver the result |
| `exec` | Run a shell command and deliver stdout to the chat; stays silent when the script produces no output |

When an `exec` job fails (non-zero exit or execution error), Moxie delivers a warning to the conversation:

```
⚠️ Scheduled task failed: <script> — Error: <reason>
```

This ensures failures are never silently dropped.

## Triggers

Use exactly one trigger per schedule:

| Flag | Description | Example |
|------|-------------|---------|
| `--in` | Relative one-shot delay from now | `--in 5m`, `--in 2h` |
| `--at` | Absolute one-shot time. Accepts RFC 3339, `YYYY-MM-DDTHH:MM`, or `YYYY-MM-DD HH:MM` | `--at 2026-03-20T10:00:00-05:00` |
| `--every` | Recurring elapsed-time interval | `--every 15m`, `--every 2h` |
| `--cron` | Recurring cron expression | `--cron "0 1 * * *"` |

Schedules use the system local timezone by default. `--every` uses elapsed-time semantics; `--cron` uses recurring wall-clock calendar semantics.

## Platform behavior

- macOS: supported minute-precision one-shot schedules, interval schedules, and portable calendar schedules are installed automatically as per-user `launchd` jobs
- macOS fallback: second-precision one-shots, far-future one-shots that need explicit year precision, and calendar shapes that do not map cleanly to `launchd` stay on the in-process scheduler automatically
- Windows: supported one-shot schedules, intervals up to 31 days, and portable calendar schedules with explicit minute/hour values are installed automatically as per-user Task Scheduler jobs
- Windows fallback: calendar schedules with wildcard minute/hour fields, month-filtered weekday schedules, or shapes that expand to too many native triggers stay on the in-process scheduler automatically
- Linux: native timer integration is not implemented yet, so schedules run on Moxie's in-process scheduler only

On Linux, you must keep `moxie serve` running if you want schedules to keep firing.

On Windows, native schedules currently register with the current interactive user token, so supported Task Scheduler schedules run while that user is signed in. Unsupported schedules still fall back to the in-process scheduler and therefore still depend on `moxie serve`.

Use `moxie schedule show <id>` to inspect sync details. When relevant, it shows fields such as `Managed by`, `Sync state`, and `Sync error` so you can tell whether a schedule is native or running via fallback.

## Portable cron subset

`--cron` accepts the common portable 5-field form:

```text
minute hour day-of-month month day-of-week
```

Supported portable forms:
- exact values like `0 9 * * 1`
- wildcards like `*`
- safe lists and ranges like `1,3,5` or `MON-FRI`
- named months and days such as `JAN` or `MON`
- safe descriptors that normalize into the portable form, such as `@daily`

Notably rejected:
- step expressions like `*/5`
- specials like `?`, `L`, `W`, `#`, and `@reboot`
- cron expressions that restrict both day-of-month and day-of-week at the same time

Moxie preserves the original cron string for display while normalizing it internally into the canonical calendar fields used by the scheduler.

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

Normal schedule management:

```bash
moxie schedule list           # List all schedules
moxie schedule show <id>      # Show details for a schedule
moxie schedule rm <id>        # Delete a schedule
```

Internal/operator trigger path:

```bash
moxie schedule fire <id>
```

`moxie schedule fire <id>` is the internal execution path used by native `launchd` and Task Scheduler jobs. It is useful for operators and tests, but normal schedule usage should go through `moxie schedule add`, `list`, `show`, and `rm`.

## Schedule limits

Each conversation can have at most 20 schedules (configurable via `max_schedules_per_conv` in `config.json`). `moxie schedule add` returns an error when the limit is reached. To prevent runaway loops where a dispatch schedule creates further schedules, a generation counter is tracked and capped at 3 (configurable via `max_schedule_generation`).

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

Without overrides, the dispatch runs against whatever backend the conversation was using when the schedule was created. `send` schedules ignore dispatch-specific overrides.
