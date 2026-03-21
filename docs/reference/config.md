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
    }
  },
  "workspaces": {
    "myapp": "/home/user/projects/myapp",
    "ops": "/home/user/projects/ops"
  },
  "subagent_max_depth": 3
}
```

## Top-level fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `channels` | object | | Transport configurations (see below) |
| `workspaces` | object | `{}` | Named directory shortcuts for `/cwd` |
| `subagent_max_depth` | int | `3` | Maximum nesting depth for subagent delegation |

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

## Conversation state

Per-conversation state is stored automatically in `~/.config/moxie/` and includes:

| Field | Description |
|-------|-------------|
| Backend | Which agent CLI to use |
| Model | Model override for the backend |
| Thread ID | Current oneagent thread |
| CWD | Working directory |
| Thinking | Reasoning effort level |

State is managed through [chat commands](../guide/commands) and persists across restarts.

## Backend configuration

Agent backends are configured through [oneagent](https://github.com/1broseidon/oneagent), not Moxie directly. Embedded defaults ship for Claude, Codex, Gemini, Pi, and OpenCode.

To customize, create `~/.config/oneagent/backends.json` with overrides. See [Backends](../guide/backends) for details.

## systemd service

For always-on operation, create `~/.config/systemd/user/moxie-serve.service`:

```ini
[Unit]
Description=Moxie chat agent

[Service]
ExecStart=%h/go/bin/moxie serve --cwd %h/projects/default
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
```

```bash
systemctl --user daemon-reload
systemctl --user enable --now moxie-serve
```
