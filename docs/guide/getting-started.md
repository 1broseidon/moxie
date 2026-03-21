# Getting Started

Moxie connects Telegram and Slack to AI coding agents. Messages you send from your phone are dispatched to agent backends — Claude, Codex, Gemini, Pi, or any CLI that [oneagent](https://github.com/1broseidon/oneagent) supports — and the response is delivered back to your chat.

## Prerequisites

- **Go 1.24+** (for `go install`) or **Homebrew**
- At least one agent CLI installed (see [Backends](./backends))
- A Telegram bot token or Slack app credentials

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

The fastest path is Telegram — two values and you're running.

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

Paste your bot token and chat ID when prompted. This creates `~/.config/moxie/config.json`.

Init can also offer to install and start Moxie as a background service for you.

### 4. Start the service

```bash
moxie serve
```

If you chose service install during `moxie init`, you can skip this step.

Send a message to your bot in Telegram. Moxie dispatches it to the default backend (Claude) and replies with the result.

## What's happening

When you send a message:

1. Moxie receives it via the Telegram or Slack transport
2. The message is dispatched to the configured agent backend (e.g. `claude -p "your message"`)
3. The agent works in the configured working directory
4. The response is delivered back to your chat

While the agent is working, you'll see a typing indicator and activity updates (tool calls, file reads, etc.) as status messages.

## Next steps

- [Set up Slack](./slack) as an additional or alternative transport
- [Configure backends](./backends) — switch agents, set default models
- [Schedule tasks](./schedules) — recurring dispatches, timed reminders
- [Chat commands](./commands) — `/model`, `/think`, `/cwd`, `/new`
