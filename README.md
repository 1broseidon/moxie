# Moxie

Chat agent service that connects Telegram and Slack to AI coding agents. Send a message from your phone, get a response from Claude, Codex, Gemini, Pi, or any other configured backend.

Moxie runs as an always-on service. Messages are dispatched to agent backends via [oneagent](https://github.com/1broseidon/oneagent), which handles the CLI invocation, output parsing, and thread management for each backend.

## Install

```bash
go install github.com/1broseidon/moxie/cmd/moxie@latest
```

Requires Go 1.24+ and at least one agent CLI installed (see [Agent backends](#agent-backends)).

## Quick start (Telegram)

The fastest way to get running:

1. **Create a Telegram bot** — open [BotFather](https://t.me/BotFather), send `/newbot`, copy the token.

2. **Get your chat ID** — send any message to your new bot, then open `https://api.telegram.org/bot<TOKEN>/getUpdates` in a browser. Find `"chat":{"id":123456}` in the response.

3. **Configure and run:**

```bash
moxie init
# Paste your bot token and chat ID when prompted

moxie serve
```

4. **Send a message** to your bot in Telegram. Moxie dispatches it to the default backend (Claude) and replies with the result.

That's it. Use `/model codex` or `/model gemini` in the chat to switch backends.

For Slack setup or advanced configuration, see below.

## Configuration

Moxie reads its config from `~/.config/moxie/config.json`. You can configure one or both transports.

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

### Both transports

You can run Telegram and Slack simultaneously:

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
  }
}
```

Then in chat: `/cwd myapp` switches the agent to that directory.

## Agent backends

Moxie dispatches to whatever agent CLIs you have installed. Backend definitions use the [oneagent](https://github.com/1broseidon/oneagent) schema, with embedded defaults plus Moxie-specific overrides in `~/.config/moxie/backends.json`.

### Supported backends

| Backend | CLI | Install |
|---------|-----|---------|
| Claude | `claude` | `npm install -g @anthropic-ai/claude-code` |
| Codex | `codex` | `npm install -g @openai/codex` |
| Gemini | `gemini` | `npm install -g @google/gemini-cli` |
| Pi | `pi` | `npm install -g @anthropics/pi` |
| OpenCode | `opencode` | See [opencode.ai](https://opencode.ai) |

Check which backends are available:

```bash
oa list
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

Moxie loads oneagent's embedded backend defaults and applies overrides from `~/.config/moxie/backends.json`. To override or add backends, create:

```json
{
  "claude": {
    "model": "opus"
  }
}
```

User overrides are merged on top of the embedded defaults. See the [oneagent docs](https://github.com/1broseidon/oneagent) for the full backend schema.

## Running as a service

### systemd (Linux)

Create `~/.config/systemd/user/moxie-serve.service`:

```ini
[Unit]
Description=Moxie chat agent

[Service]
ExecStart=%h/go/bin/moxie serve --cwd %h/projects/default
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

Create `~/Library/LaunchAgents/io.github.1broseidon.moxie.plist`:

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
    <string>--cwd</string>
    <string>/Users/you/projects/default</string>
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

Then use:

```bash
moxie service start
moxie service stop
moxie service restart
moxie service reload
moxie service status
```

### serve flags

```
moxie serve [--cwd <dir>] [--transport <telegram|slack>]
```

| Flag | Description |
|------|-------------|
| `--cwd` | Default working directory for agent backends |
| `--transport` | Run only one transport instead of both |

## Chat commands

Commands available in Telegram and Slack:

| Command | Description |
|---------|-------------|
| `/new [backend] [workspace]` | Start a new conversation thread |
| `/model [backend] [model]` | Show or switch the agent backend |
| `/think [off\|low\|medium\|high]` | Show or set thinking/reasoning effort |
| `/cwd [name\|path]` | Show or switch working directory |
| `/threads [name]` | List or switch threads |
| `/compact` | Compact the current thread |

## Schedules

Schedule one-time or recurring messages and dispatches:

```bash
# Remind me in 5 minutes
moxie schedule add --transport telegram --action send --in 5m --text "Call John"

# Daily security scan at 1am
moxie schedule add --transport slack --action dispatch --cron "0 1 * * *" --text "Run a security scan"

# One-shot dispatch at a specific time
moxie schedule add --transport telegram --action dispatch --at 2026-03-20T10:00:00-05:00 --text "Check deploy status"

# List and manage
moxie schedule list
moxie schedule show <id>
moxie schedule rm <id>
```

## Subagents

Delegate work to a different backend in the background:

```bash
moxie subagent --backend codex --text "Write tests for internal/auth"
```

The primary agent can also delegate via the `moxie subagent` CLI tool during a conversation. Results are synthesized back into the parent conversation thread.

## CLI reference

```
moxie init                                          Configure bot token and chat ID
moxie send [--transport <telegram|slack>] <message> Send a message
moxie messages [--json|--raw] [-n N]                List recent messages
moxie msg                                           Alias for messages
moxie schedule <add|list|show|rm>                   Manage schedules
moxie subagent --backend <name> --text <task>       Delegate to a background agent
moxie result <subcommand>                           Retrieve subagent results
moxie threads show <id>                             Show thread turns
moxie service <subcommand>                          Control the background service
moxie serve [--cwd <dir>] [--transport <t>]         Run chat transports
```

## License

MIT
