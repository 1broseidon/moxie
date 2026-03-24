# Slack

Moxie connects to Slack using Socket Mode — no public URL or ngrok needed. The bot runs locally and maintains a persistent WebSocket connection.

## Setup

### 1. Create a Slack app

Go to [api.slack.com/apps](https://api.slack.com/apps) and click **Create New App** → **From scratch**. Name it (e.g. "Moxie") and select your workspace.

### 2. Configure bot scopes

Under **OAuth & Permissions**, add these **Bot Token Scopes**:

| Scope | Purpose |
|-------|---------|
| `chat:write` | Send messages |
| `channels:history` | Read channel messages |
| `groups:history` | Read private channel messages |
| `im:history` | Read direct messages |
| `files:write` | Upload file attachments |

### 3. Enable Socket Mode

Under **Socket Mode**, toggle it on. Create an app-level token with the `connections:write` scope. Copy the token — it starts with `xapp-`.

### 4. Subscribe to events

Under **Event Subscriptions**, enable events and add these **Bot Events**:

- `message.channels`
- `message.groups`
- `message.im`

### 5. Install the app

Click **Install to Workspace** and authorize. Copy the **Bot User OAuth Token** from the OAuth & Permissions page — it starts with `xoxb-`.

### 6. Invite the bot

In Slack, invite the bot to your channel:

```
/invite @moxie
```

### 7. Configure Moxie

Add to `~/.config/moxie/config.json`:

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

## Configuration reference

| Field | Required | Description |
|-------|----------|-------------|
| `provider` | Yes | Must be `"slack"` |
| `token` | Yes | Bot User OAuth Token (`xoxb-...`) |
| `app_token` | Yes | App-Level Token (`xapp-...`) for Socket Mode |
| `channel_id` | No | Default channel for scheduled messages |

## Features

### Message formatting

Moxie instructs the agent to use Slack mrkdwn:

- `*bold*`, `_italic_`, `~strikethrough~`
- `` `inline code` ``, ` ```code blocks``` `
- Lists and plain links

### Long messages

Responses longer than 35,000 characters are split into multiple messages at paragraph boundaries.

### Thread replies

Slack messages from threaded conversations are dispatched with thread context. Replies go back to the same thread.

### Activity status

While the agent is working, Moxie posts a status message showing the current tool activity. The status is edited in-place as activity changes, then deleted when the response arrives.

## Running only Slack

```bash
moxie serve --transport slack
```

## Multiple transports

You can run Telegram, Slack, and Webex simultaneously:

```json
{
  "channels": {
    "telegram": {
      "provider": "telegram",
      "token": "...",
      "channel_id": "..."
    },
    "slack": {
      "provider": "slack",
      "token": "xoxb-...",
      "app_token": "xapp-..."
    }
  }
}
```

Each transport maintains its own conversation state, threads, and backend selection.
