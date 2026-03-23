# Cross-Platform Schedule Abstraction

Status: implemented, with Linux native backend intentionally deferred

This note records the shipped schedule architecture behind `moxie schedule`.

## What shipped

Moxie now presents one schedule model across platforms while choosing the best available runtime automatically:

- canonical trigger types: `at`, `interval`, and `calendar`
- first-class CLI flags: `--at`, `--in`, `--every`, and `--cron`
- portable cron normalization into the canonical calendar model
- automatic backend reconciliation and sync metadata
- native per-user `launchd` materialization on macOS for supported schedules
- native per-user Task Scheduler materialization on Windows for supported schedules
- in-process fallback on unsupported shapes or native install/update failures
- internal execution entrypoint: `moxie schedule fire <id>`

Users do not need a separate install or sync step for schedules.

## Canonical model

Moxie owns the source of truth for schedules in its own store. Platform schedulers are projections of that model.

Canonical trigger types:

- `at` — one-shot exact time
- `interval` — every N duration
- `calendar` — recurring schedule expressed as structured calendar fields

Cron remains a supported input syntax, but it is parsed into the canonical `calendar` form rather than used as the primary internal representation.

## Trigger semantics

### `at`

- one-shot exact time
- `--in` is resolved into an `at` schedule at creation time
- native backends may self-remove after the schedule fires

### `interval`

- recurring elapsed-time semantics
- minimum supported portable granularity is 1 minute
- represented canonically as a duration, not a wall-clock calendar pattern

### `calendar`

- recurring wall-clock semantics in the system local timezone by default
- backed by structured calendar fields internally
- `--cron` is normalized into this model

## Cron handling

Moxie accepts the common portable 5-field form:

```text
minute hour day-of-month month day-of-week
```

Portable forms supported canonically:

- exact values
- wildcards
- safe lists and ranges
- named months and weekdays
- safe descriptors such as `@daily`

Rejected forms:

- step expressions such as `*/5`
- specials such as `?`, `L`, `W`, `#`, and `@reboot`
- expressions that restrict both day-of-month and day-of-week at once

The original cron string is preserved for display, while scheduling uses the normalized canonical calendar fields.

## Backend model

Platform scheduler integration is an internal detail. Moxie automatically materializes schedules into native backends when available and falls back to the in-process scheduler when not.

Current behavior:

- macOS: supported schedules materialize into per-user `launchd` jobs
- Windows: supported schedules materialize into per-user Task Scheduler jobs
- Linux: native timer integration is not implemented yet; fallback remains first-class

Sync metadata on each schedule records:

- `managed_by`
- `state`
- `error`

This metadata explains whether the schedule is running natively or via fallback, and why a fallback happened.

## Execution entrypoint

Native schedulers invoke:

```bash
moxie schedule fire <id>
```

That command is internal/operator plumbing. It keeps execution semantics owned by Moxie while letting OS-native schedulers handle wake-up and trigger timing.

## Fallback rules

Fallback behavior is intentional, not an error by itself.

Moxie falls back to the in-process scheduler when:

- the current platform has no native backend
- the trigger shape cannot be translated safely
- native installation or update fails

Fallbacks remain visible through schedule sync metadata.

## Platform notes

### macOS

Supported schedules are projected into `launchd` automatically.

Known fallback cases include:

- second-precision one-shots
- one-shots that require explicit year precision `launchd` cannot preserve
- calendar shapes that do not map cleanly to `StartCalendarInterval`

### Windows

Supported schedules are projected into Task Scheduler automatically.

Known fallback cases include:

- intervals longer than 31 days
- calendar schedules without explicit minute and hour values
- month-filtered weekday schedules in portable calendar mode
- schedules that would expand into too many native triggers

Windows native schedules currently use the current interactive user token.

### Linux

Linux intentionally remains on the in-process scheduler for now. No Linux native backend work was included in this rollout.

## Compatibility and migration

The implementation keeps backward-compatible loading for existing schedule files.

- legacy cron-backed schedules are normalized into `calendar`
- additive sync metadata does not break older files
- old schedules continue to run via fallback where needed

## Non-goals for this rollout

- no Linux native scheduler backend
- no functional behavior changes beyond docs/help cleanup in the wrap-up pass
- no user-facing schedule install or repair workflow in the happy path
