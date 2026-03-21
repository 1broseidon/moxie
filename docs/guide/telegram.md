# Telegram

Moxie connects to Telegram using the Bot API with long polling. No webhooks, no public URLs needed.

## Setup

### 1. Create a bot

Open [BotFather](https://t.me/BotFather) in Telegram and send `/newbot`. Choose a display name and a username ending in `bot`. BotFather returns a token like `123456789:AAH...`.

### 2. Get your chat ID

Send any message to your new bot, then visit:

```
https://api.telegram.org/bot<TOKEN>/getUpdates
```

In the JSON response, find your chat ID at `result[0].message.chat.id`. It's a numeric value like `412407481`.

### 3. Configure

Add to `~/.config/moxie/config.json`:

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

Or use `moxie init` for interactive setup.

## Configuration reference

| Field | Required | Description |
|-------|----------|-------------|
| `provider` | Yes | Must be `"telegram"` |
| `token` | Yes | Bot token from BotFather |
| `channel_id` | Yes | Numeric chat ID for your conversation |

## Features

### Message formatting

Moxie instructs the agent to format responses in Telegram HTML:

- `<b>bold</b>`, `<i>italic</i>`, `<code>inline</code>`
- `<pre>code blocks</pre>`
- `<a href="url">links</a>`

If HTML parsing fails (e.g. malformed tags from the agent), the message is automatically resent as plain text.

### Long messages

Responses longer than 4000 characters are split into multiple messages at paragraph boundaries, then line boundaries, then word boundaries.

### File attachments

The agent can send files back by including `<send>/path/to/file</send>` in its response. Images (`.jpg`, `.png`, `.gif`, `.webp`) are sent as photos; other files as documents.

### Inbound photos

Photos sent to the bot are downloaded and passed to the agent as file paths. The agent can read the file to analyze the image.

### Typing indicator

While the agent is working, Moxie sends a continuous typing indicator. Activity updates (tool calls, file operations) appear as status messages that are cleaned up when the response arrives.

## Running only Telegram

If you have both transports configured but want to run only Telegram:

```bash
moxie serve --transport telegram
```
