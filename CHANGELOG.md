# Changelog

All notable changes to Moxie are documented here.

## [0.1.4] - 2026-03-21

### Added

- macOS `launchd` support for `moxie service start|stop|restart|reload|status`
- LaunchAgent documentation for `~/Library/LaunchAgents/io.github.1broseidon.moxie.plist`

### Changed

- macOS service control now follows the per-user LaunchAgent model instead of failing through `systemctl`

## [0.1.3] - 2026-03-21

### Changed

- Service lifecycle commands now live under `moxie service ...` instead of top-level `moxie start|stop|restart|reload|status`
- Non-Linux platforms now get a clear service-manager error instead of attempting to invoke `systemctl`

## [0.1.2] - 2026-03-21

### Added

- Top-level `moxie start`, `stop`, `restart`, `reload`, and `status` commands for user service control
- In-process `SIGHUP` reload support for `moxie serve` so config and backend definitions can be reloaded without exiting the process

### Changed

- Backend prompts now frame Claude, Codex, and other backends as operating on behalf of Moxie
- Capability descriptions are guided toward local CLI behavior and user-facing Moxie features instead of harness-centric language
- `poll` and `cursor` are now documented as operator/debug commands instead of primary user commands

### Fixed

- Release workflow can now be manually dispatched for an existing tag
- Homebrew tap publishing path was validated and backfilled
- Prompt injection tests now allow the backend identity suffix added to system prompts

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
