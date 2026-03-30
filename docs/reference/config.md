# Configuration

Moxie reads its config from `~/.config/moxie/config.json`.

## Full example

```json
{
  "channels": {
    "telegram": {
      "provider": "telegram",
      "token": "123456789:AAH...",
      "channel_id": "412407481"
    },
    "slack": {
      "provider": "slack",
      "token": "xoxb-...",
      "app_token": "xapp-...",
      "channel_id": "C0123456789"
    },
    "webex": {
      "provider": "webex",
      "token": "Y2lzY29zcGFyazovL3VzL1RPS0VOL...",
      "bot_id": "Y2lzY29zcGFyazovL3VzL1BFT1BMRS8...",
      "channel_id": "Y2lzY29zcGFyazovL3VzL1JPT00v..."
    }
  },
  "workspaces": {
    "myapp": "/home/user/projects/myapp",
    "ops": "/home/user/projects/ops"
  },
  "default_cwd": "/home/user/.local/share/moxie/workspace",
  "recover_pending_jobs_on_startup": false,
  "run_overdue_schedules_on_startup": false,
  "subagent_max_depth": 3,
  "max_pending_subagents": 5,
  "max_schedules_per_conv": 20,
  "max_jobs_per_minute": 10,
  "max_schedule_generation": 3
}
```

## Top-level fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `channels` | object | | Transport configurations (see below) |
| `workspaces` | object | `{}` | Named directory shortcuts for `/cwd` |
| `default_cwd` | string | platform-specific workspace | Default working directory when no conversation override or explicit `--cwd` is set |
| `recover_pending_jobs_on_startup` | bool | `true` | When `false`, discard persisted pending jobs on startup instead of replaying them |
| `run_overdue_schedules_on_startup` | bool | `true` | When `false`, skip missed in-process schedule executions on startup instead of catching them up immediately |
| `subagent_max_depth` | int | `3` | Maximum nesting depth for subagent delegation |
| `max_pending_subagents` | int | `5` | Maximum concurrent subagent jobs per conversation — dispatch is rejected with an error when this limit is reached |
| `max_schedules_per_conv` | int | `20` | Maximum schedules per conversation — `moxie schedule add` is rejected when this limit is reached |
| `max_jobs_per_minute` | int | `10` | Rate limit on `moxie send` and subagent dispatch (jobs per minute per process) |
| `max_schedule_generation` | int | `3` | Maximum schedule→dispatch→schedule recursion depth — prevents runaway loops where a dispatch schedule creates more schedules |

If you do not want Moxie to resume interrupted jobs or immediately run missed Linux fallback schedules after a restart, set both startup flags to `false` and restart the service.

## Channel: Telegram

| Field | Required | Description |
|-------|----------|-------------|
| `provider` | Yes | `"telegram"` |
| `token` | Yes | Bot token from [BotFather](https://t.me/BotFather) |
| `channel_id` | Yes | Numeric Telegram chat ID |

## Channel: Slack

| Field | Required | Description |
|-------|----------|-------------|
| `provider` | Yes | `"slack"` |
| `token` | Yes | Bot User OAuth Token (`xoxb-...`) |
| `app_token` | Yes | App-Level Token (`xapp-...`) for Socket Mode |
| `channel_id` | No | Default channel for scheduled messages |

## Channel: Webex

Webex support is currently **1:1 direct-message only**. Group spaces are intentionally ignored by the transport.

| Field | Required | Description |
|-------|----------|-------------|
| `provider` | Yes | `"webex"` |
| `token` | Yes | Webex bot token |
| `bot_id` | No | Bot person ID. Optional shortcut; Moxie can discover it from the token. |
| `channel_id` | No | Default direct room ID for `moxie send --transport webex` and schedules |
| `allowed_user_ids` | No | Allowlist of Webex person IDs permitted to talk to the bot |
| `allowed_emails` | No | Allowlist of email addresses permitted to talk to the bot |

## Conversation state

Per-conversation state is stored automatically in `~/.config/moxie/` and includes:

| Field | Description |
|-------|-------------|
| Backend | Which agent CLI to use |
| Model | Model override for the backend |
| Thread ID | Current conversation thread |
| CWD | Working directory |
| Thinking | Reasoning effort level |

State is managed through [chat commands](../guide/commands) and persists across restarts.

Platform workspace defaults:

- Linux: `~/.local/share/moxie/workspace`
- macOS: `~/Library/Application Support/Moxie/workspace`
- Windows: `%LocalAppData%\Moxie\workspace`

## Backend configuration

Moxie loads embedded backend defaults and applies overrides from `~/.config/moxie/backends.json`.

To customize, create `~/.config/moxie/backends.json`. See [Backends](../guide/backends) for details.

## systemd service

For always-on operation, create `~/.config/systemd/user/moxie-serve.service`, or let `moxie init` / `moxie service install` generate it:

```ini
[Unit]
Description=Moxie chat agent

[Service]
WorkingDirectory=%h/.local/share/moxie/workspace
ExecStart=%h/go/bin/moxie serve
ExecReload=/bin/kill -HUP $MAINPID
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
```

```bash
systemctl --user daemon-reload
systemctl --user enable --now moxie-serve
```

For day-to-day control you can also use:

```bash
moxie service start
moxie service stop
moxie service restart
moxie service reload
moxie service status
```

## launchd service

For macOS, create `~/Library/LaunchAgents/io.github.1broseidon.moxie.plist`, or let `moxie init` / `moxie service install` generate it. The LaunchAgent label should be `io.github.1broseidon.moxie` and `ProgramArguments` should use an absolute path to the `moxie` binary.

Recommended keys:

- `ProgramArguments`
- `WorkingDirectory`
- `RunAtLoad`
- `KeepAlive`
- `StandardOutPath`
- `StandardErrorPath`

Once the plist exists, use:

```bash
moxie service start
moxie service stop
moxie service restart
moxie service reload
moxie service status
```

When generated via `moxie service install`, the LaunchAgent also captures the current `PATH` and `HOME` so backend CLIs remain available outside your interactive shell session. The service working directory comes from `--cwd`, otherwise `default_cwd`, otherwise the platform workspace default.

Windows still does not have native `moxie service install` / service control support. On macOS, Moxie materializes supported schedules into per-user `launchd` jobs automatically and falls back to the in-process scheduler when a schedule shape cannot be represented exactly. On Windows, Moxie now materializes supported one-shot, interval, and concrete-time portable calendar schedules into per-user Task Scheduler jobs automatically, with the same fallback behavior for unsupported schedule shapes or Task Scheduler install/update failures. Windows native schedules currently use the current interactive user token, so they run while that user is signed in.
