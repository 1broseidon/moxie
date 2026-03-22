# Changelog

All notable changes to Moxie are documented here.

## [0.1.10] - 2026-03-21

### Changed

- Bumped `oneagent` to `v0.11.10`

### Fixed

- Codex subagents now inherit the backend-agnostic `oneagent` template fix for empty inline assignment args, preventing `exit status 1` when thinking is unset

## [0.1.9] - 2026-03-21

### Added

- Configurable `default_cwd` with platform-specific managed workspace defaults for Linux and macOS

### Changed

- `moxie init` now prompts for a default workspace and can install the service against that configured default
- `moxie serve` now prefers an explicit `--cwd`, then the current shell directory, then `default_cwd`, then the platform workspace default
- `moxie service install` now uses the service manager working directory instead of passing `--cwd` to `moxie serve`
- README and docs now emphasize the service-first setup flow and remove internal `oneagent`/`oa list` references from user-facing guidance

### Fixed

- User-facing docs now match actual `/cwd` behavior for named workspaces

## [0.1.8] - 2026-03-22

### Fixed

- LaunchAgent installs now capture `PATH` and `HOME` so backend CLIs remain available when Moxie runs under `launchd`

## [0.1.7] - 2026-03-21

### Fixed

- LaunchAgent installs now prefer the stable `moxie` path from `PATH` instead of a versioned Homebrew Cellar path

### Changed

- `moxie service install`, `start`, `stop`, `uninstall`, `restart`, and `reload` now print explicit confirmation messages on success

## [0.1.6] - 2026-03-21

### Added

- `moxie service install` and `moxie service uninstall` for native service definition management
- Optional service install/start flow during `moxie init`

### Changed

- `moxie init` can now collect a default service working directory when installing a background service

## [0.1.5] - 2026-03-21

### Fixed

- Windows cross-build compatibility for the macOS launchd reload path

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
