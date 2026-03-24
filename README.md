# Moxie

Chat agent service that connects Telegram, Slack, and Webex to AI coding agents. Send a message from your phone, get a response from Claude, Codex, Gemini, Pi, or any other configured backend.

Moxie runs as an always-on service. Messages are dispatched to the configured agent backend CLI and the result is delivered back to Telegram, Slack, or Webex.

## Install

```bash
go install github.com/1broseidon/moxie/cmd/moxie@latest
```

Requires Go 1.24+ and at least one agent CLI installed (see [Agent backends](#agent-backends)).

## Quick start (Telegram)

The recommended path is to let Moxie install itself as a background service during init on Linux or macOS. On Windows, use `moxie init` plus foreground `moxie serve` for now.

1. **Create a Telegram bot** — open [BotFather](https://t.me/BotFather), send `/newbot`, copy the token.

2. **Get your chat ID** — send any message to your new bot, then open `https://api.telegram.org/bot<TOKEN>/getUpdates` in a browser. Find `"chat":{"id":123456}` in the response.

3. **Configure Moxie:**

```bash
moxie init
# Paste your bot token and chat ID when prompted
# Choose a default workspace path
# Say yes to install and start the background service
```

4. **Verify the service if needed:**

```bash
moxie service status
```

5. **If you skipped service install during init, run Moxie manually instead:**

```bash
moxie serve
```

6. **Send a message** to your bot in Telegram. Moxie dispatches it to the default backend (Claude) and replies with the result.

That's it. Use `/model codex` or `/model gemini` in the chat to switch backends.

For most users, the service-first setup is the best default. Use foreground `moxie serve` mainly when you intentionally want Moxie tied to the current project directory in your shell.

For Slack, Webex, or advanced configuration, see below.

## Configuration

Moxie reads its config from `~/.config/moxie/config.json`. You can configure one or more transports.

### Telegram

The quick start above covers the interactive setup. To configure manually:

```json
{
  "channels": {
    "telegram": {
      "provider": "telegram",
      "token": "123456789:AAH...",
      "channel_id": "412407481"
    }
  }
}
```

| Field | Description |
|-------|-------------|
| `token` | Bot token from BotFather |
| `channel_id` | Your Telegram chat ID (the numeric ID, not the username) |

### Slack

Slack uses Socket Mode, so no public URL is needed.

1. Create a Slack app at [api.slack.com/apps](https://api.slack.com/apps).

2. Under **OAuth & Permissions**, add these bot token scopes:
   - `chat:write`
   - `channels:history`
   - `groups:history`
   - `im:history`
   - `files:write`

3. Under **Socket Mode**, enable it and generate an app-level token with the `connections:write` scope.

4. Under **Event Subscriptions**, enable events and subscribe to:
   - `message.channels`
   - `message.groups`
   - `message.im`

5. Install the app to your workspace. Copy the **Bot User OAuth Token** (`xoxb-...`) and the **App-Level Token** (`xapp-...`).

6. Invite the bot to a channel: `/invite @yourbot`

7. Add to your config:

```json
{
  "channels": {
    "slack": {
      "provider": "slack",
      "token": "xoxb-...",
      "app_token": "xapp-..."
    }
  }
}
```

| Field | Description |
|-------|-------------|
| `token` | Bot User OAuth Token (`xoxb-...`) |
| `app_token` | App-Level Token (`xapp-...`) for Socket Mode |
| `channel_id` | Optional. Default Slack channel for scheduled messages |

### Webex

Webex support is currently **1:1 direct-message only**. Group spaces are intentionally ignored.

Add to your config:

```json
{
  "channels": {
    "webex": {
      "provider": "webex",
      "token": "Y2lzY29zcGFyazovL3VzL1RPS0VOL...",
      "bot_id": "Y2lzY29zcGFyazovL3VzL1BFT1BMRS8..."
    }
  }
}
```

| Field | Description |
|-------|-------------|
| `token` | Webex bot token |
| `bot_id` | Optional. Bot person ID |
| `channel_id` | Optional. Default direct room ID for `moxie send --transport webex` and schedules |
| `allowed_user_ids` | Optional. Allowlist of Webex person IDs permitted to use the bot |
| `allowed_emails` | Optional. Allowlist of email addresses permitted to use the bot |

### Multiple transports

You can run Telegram, Slack, and Webex simultaneously:

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
      "app_token": "xapp-..."
    },
    "webex": {
      "provider": "webex",
      "token": "Y2lzY29zcGFyazovL3VzL1RPS0VOL...",
      "bot_id": "Y2lzY29zcGFyazovL3VzL1BFT1BMRS8..."
    }
  }
}
```

### Workspaces

Workspaces let you switch the agent's working directory with `/cwd`:

```json
{
  "channels": { ... },
  "workspaces": {
    "myapp": "/home/user/projects/myapp",
    "ops": "/home/user/projects/ops"
  },
  "default_cwd": "/home/user/.local/share/moxie/workspace"
}
```

Then in chat: `/cwd myapp` switches the agent to that directory.

You can also set a global fallback workspace with `default_cwd`. When no conversation-specific `/cwd` is active, Moxie uses:

- the explicit `--cwd` passed to `moxie serve`, if any
- otherwise the current shell directory for foreground `moxie serve`
- otherwise `default_cwd`
- otherwise the platform default workspace

## Agent backends

Moxie dispatches to whichever supported agent CLIs you have installed. It ships with built-in backend definitions and lets you override them in `~/.config/moxie/backends.json`.

### Supported backends

| Backend | CLI | Install |
|---------|-----|---------|
| Claude | `claude` | `npm install -g @anthropic-ai/claude-code` |
| Codex | `codex` | `npm install -g @openai/codex` |
| Gemini | `gemini` | `npm install -g @google/gemini-cli` |
| Pi | `pi` | `npm install -g @anthropics/pi` |
| OpenCode | `opencode` | See [opencode.ai](https://opencode.ai) |

After installing a backend CLI, make sure it is on your `PATH`:

```bash
command -v claude
command -v codex
command -v gemini
command -v pi
command -v opencode
```

### Switching backends

In chat, use `/model` to switch:

```
/model claude           # Switch to Claude
/model codex            # Switch to Codex
/model gemini           # Switch to Gemini
/model claude sonnet    # Switch to Claude with a specific model
/model pi grok3         # Switch to Pi with Grok 3
```

The backend and model are persisted per conversation.

### Thinking levels

For backends that support reasoning effort (Claude, Codex, Pi):

```
/think high      # Extended thinking
/think medium    # Balanced
/think low       # Fast
/think off       # Disable (default)
```

### Custom backend config

Moxie loads its built-in backend defaults and applies overrides from `~/.config/moxie/backends.json`. To override or add backends, create:

```json
{
  "claude": {
    "model": "opus"
  }
}
```

User overrides are merged on top of the embedded defaults.

## Running as a service

### systemd (Linux)

Create `~/.config/systemd/user/moxie-serve.service` yourself, or let `moxie init` / `moxie service install` generate it for you.

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

Check status:

```bash
systemctl --user status moxie-serve
```

Or use the built-in wrappers:

```bash
moxie service start
moxie service stop
moxie service restart
moxie service reload
moxie service status
```

### launchd (macOS)

Create `~/Library/LaunchAgents/io.github.1broseidon.moxie.plist` yourself, or let `moxie init` / `moxie service install` generate it for you:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>io.github.1broseidon.moxie</string>
  <key>ProgramArguments</key>
  <array>
    <string>/opt/homebrew/bin/moxie</string>
    <string>serve</string>
  </array>
  <key>WorkingDirectory</key>
  <string>/Users/you/projects/default</string>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>/Users/you/Library/Logs/moxie.log</string>
  <key>StandardErrorPath</key>
  <string>/Users/you/Library/Logs/moxie.log</string>
</dict>
</plist>
```

Replace the binary path with your actual `moxie` install path.

If you use `moxie service install`, Moxie will also capture the current `PATH` and `HOME` into the LaunchAgent so backend CLIs like `claude` and `codex` remain available when running as a service. Its working directory comes from `--cwd`, otherwise `default_cwd`, otherwise the platform workspace default.

Then use:

```bash
moxie service start
moxie service stop
moxie service restart
moxie service reload
moxie service status
```

### Windows status

Windows still does not have native `moxie service install` / service control support, so chat handling still relies on foreground `moxie serve`. Supported schedules are materialized into per-user Task Scheduler jobs automatically with no separate sync step, and unsupported shapes fall back to Moxie's in-process scheduler with the fallback reason recorded in schedule metadata. Windows native schedules currently use the interactive user token, so they run while that user is signed in.

### serve flags

```
moxie serve [--cwd <dir>] [--transport <telegram|slack|webex>]
```

| Flag | Description |
|------|-------------|
| `--cwd` | Explicit working directory override. Without it, `moxie serve` prefers the current shell directory, then `default_cwd`, then the platform workspace default. |
| `--transport` | Run only one transport instead of both |

## Chat commands

Commands available in Telegram, Slack, and Webex direct messages:

| Command | Description |
|---------|-------------|
| `/new [backend] [workspace]` | Start a new conversation thread |
| `/model [backend] [model]` | Show or switch the agent backend |
| `/think [off\|low\|medium\|high]` | Show or set thinking/reasoning effort |
| `/cwd [name]` | Show the current directory or switch to a named workspace |
| `/threads [name]` | List or switch threads |
| `/compact` | Compact the current thread |

## Schedules

Schedule one-time or recurring messages and dispatches:

```bash
# Remind me in 5 minutes (one-shot relative)
moxie schedule add --transport telegram --action send --in 5m --text "Call John"

# Check queue depth every 30 minutes (recurring interval)
moxie schedule add --transport telegram --action dispatch --every 30m --text "Check queue depth"

# Daily security scan at 1am (recurring calendar)
moxie schedule add --transport slack --action dispatch --cron "0 1 * * *" --text "Run a security scan"

# One-shot dispatch at a specific time
moxie schedule add --transport telegram --action dispatch --at 2026-03-20T10:00:00-05:00 --text "Check deploy status"

# List and manage
moxie schedule list
moxie schedule show <id>
moxie schedule rm <id>
```

Supported schedules are materialized automatically into per-user `launchd` jobs on macOS and per-user Task Scheduler jobs on Windows when possible. Unsupported schedule shapes fall back to Moxie's in-process scheduler.

`moxie schedule fire <id>` exists as internal/operator plumbing used by those native backends. Most users should manage schedules through `add`, `list`, `show`, and `rm` instead.

## Subagents

Delegate work to a different backend in the background:

```bash
moxie subagent --backend codex --text "Write tests for internal/auth"
```

The primary agent can also delegate via the `moxie subagent` CLI tool during a conversation. Results are synthesized back into the parent conversation thread.

## CLI reference

```
moxie init                                          Configure chat credentials and optionally install/start the service
moxie send [--transport <telegram|slack|webex>] <message> Send a message
moxie messages [--json|--raw] [-n N]                List recent messages
moxie msg                                           Alias for messages
moxie schedule <add|list|show|rm>                   Manage schedules
moxie subagent --backend <name> --text <task>       Delegate to a background agent
moxie result <subcommand>                           Retrieve subagent results
moxie threads show <id>                             Show thread turns
moxie service <subcommand>                          Install or control the background service
moxie serve [--cwd <dir>] [--transport <t>]         Run chat transports
```

## License

MIT
