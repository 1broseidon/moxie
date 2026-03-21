# Changelog

All notable changes to Moxie are documented here.

## [0.1.0] - 2026-03-20

Initial public release.

### Added

- Telegram transport with long polling, HTML formatting, photo handling, and file attachments
- Slack transport with Socket Mode, mrkdwn formatting, and thread support
- Multi-backend dispatch via oneagent (Claude, Codex, Gemini, Pi, OpenCode)
- `/model` command to switch backends and models per conversation
- `/think` command for reasoning effort control (low/medium/high)
- `/cwd` command with named workspaces
- `/new` and `/threads` for thread management
- `/compact` for thread compaction
- Schedules with one-shot (`--in`, `--at`) and recurring (`--cron`) triggers
- Subagent delegation with synthesis back to parent conversation
- Paragraph-aware message chunking for long responses
- Activity status messages during dispatch
- Job persistence and recovery across restarts
- systemd service support
