# Getting Started

Moxie connects Telegram, Slack, and Webex to AI coding agents. Messages you send from your phone or chat client are dispatched to supported agent backends like Claude, Codex, Gemini, Pi, and OpenCode, and the response is delivered back to your chat.

## Prerequisites

- **Go 1.24+** (for `go install`) or **Homebrew**
- At least one agent CLI installed (see [Backends](./backends))
- A Telegram bot token, Slack app credentials, or a Webex bot token

## Install

::: code-group
```bash [Homebrew]
brew install 1broseidon/tap/moxie
```
```bash [Go]
go install github.com/1broseidon/moxie/cmd/moxie@latest
```
:::

## Configure

The recommended path is Telegram plus service install during `moxie init`.

Note: the service-first flow currently applies to Linux and macOS. On Windows, native service install/control is still not implemented, so foreground `moxie serve` remains the way to keep chat handling running. Supported schedules are still materialized into Task Scheduler automatically, while unsupported schedule shapes fall back to the in-process scheduler.

### 1. Create a Telegram bot

Open [BotFather](https://t.me/BotFather) in Telegram and send `/newbot`. Give it a name and a username ending in `bot`. Copy the token it returns.

### 2. Get your chat ID

Send any message to your new bot, then open this URL in a browser (replace `<TOKEN>` with your bot token):

```
https://api.telegram.org/bot<TOKEN>/getUpdates
```

Find `"chat":{"id":123456}` in the JSON response. That number is your chat ID.

### 3. Run init

```bash
moxie init
```

Paste your bot token and chat ID when prompted. Init also asks for a default workspace path, writes everything to `~/.config/moxie/config.json`, and can install and start Moxie as a background service for you.

For the preferred quick start, just say yes to service install/start during `moxie init`.

### 4. Verify the service if needed

If you said yes during `moxie init`, Moxie should already be running in the background. You can confirm with:

```bash
moxie service status
```

### 5. Manual fallback: run in the foreground

If you intentionally skipped service install, you can still run Moxie manually:

```bash
moxie serve
```

Use foreground `moxie serve` mainly when you want Moxie tied to the current shell directory for project-local work.

Send a message to your bot in Telegram. Moxie dispatches it to the default backend (Claude) and replies with the result.

## Other transports

### Slack

Slack uses Socket Mode and is configured manually in `~/.config/moxie/config.json`.

See [Slack](./slack).

### Webex

Webex is configured manually in `~/.config/moxie/config.json`.

Current limitation: **Webex support is 1:1 direct-message only**. Group spaces are intentionally ignored for now.

See [Webex](./webex).

## What's happening

When you send a message:

1. Moxie receives it via the Telegram, Slack, or Webex transport
2. The message is dispatched to the configured agent backend (e.g. `claude -p "your message"`)
3. The agent works in the current conversation directory, or falls back to the configured default workspace
4. The response is delivered back to your chat

While the agent is working, you may see typing indicators or status messages depending on the transport.

## Next steps

- [Set up Slack](./slack) as an additional or alternative transport
- [Set up Webex](./webex) for 1:1 direct-message chat
- [Configure backends](./backends) — switch agents, set default models
- [Schedule tasks](./schedules) — recurring dispatches, timed reminders
- [Chat commands](./commands) — `/model`, `/think`, `/cwd`, `/new`
