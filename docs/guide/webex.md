# Webex

Moxie can connect to Webex using a bot token.

## Current scope

Webex support is currently **1:1 direct-message only**.

- Direct chats with the bot are supported
- Group spaces are intentionally ignored
- `moxie send --transport webex` and Webex schedules require a direct room ID if you want a default target

## 1. Create a Webex bot

Go to [developer.webex.com/my-apps](https://developer.webex.com/my-apps) and create a bot.

Copy the bot token.

Optionally copy the bot person ID as well. Moxie can discover it automatically from the token, but storing it explicitly is fine.

## 2. Configure Moxie

Add a `webex` channel to `~/.config/moxie/config.json`:

```json
{
  "channels": {
    "webex": {
      "provider": "webex",
      "token": "Y2lzY29zcGFyazovL3VzL1RPS0VOL...",
      "bot_id": "Y2lzY29zcGFyazovL3VzL1BFT1BMRS8...",
      "allowed_user_ids": [
        "Y2lzY29zcGFyazovL3VzL1BFT1BMRS8..."
      ]
    }
  }
}
```

### Optional: default Webex room for `send` and schedules

If you want to use:

- `moxie send --transport webex ...`
- `moxie schedule add --transport webex ...`

then add `channel_id` with a **direct room ID**:

```json
{
  "channels": {
    "webex": {
      "provider": "webex",
      "token": "...",
      "bot_id": "...",
      "channel_id": "Y2lzY29zcGFyazovL3VzL1JPT00v..."
    }
  }
}
```

If you do not set `channel_id`, Webex still works for inbound 1:1 chats started from Webex itself.

If you want to restrict who can use the bot, set `allowed_user_ids` and/or `allowed_emails`. When either allowlist is present, only matching senders are processed.

## 3. Start Moxie

```bash
moxie serve --transport webex
```

Or run all configured transports:

```bash
moxie serve
```

## 4. Start chatting

Open a **direct 1:1 chat** with your bot in Webex and send a message.

Moxie will:

1. Poll direct Webex rooms
2. Ignore group spaces
3. Dispatch your DM to the configured backend
4. Reply in the same direct chat

## Default room IDs

If you need a default `channel_id`, use the Webex API to list direct rooms:

```bash
curl -s https://webexapis.com/v1/rooms?type=direct \
  -H "Authorization: Bearer $WEBEX_BOT_TOKEN"
```

Use the direct room's `id` as `channels.webex.channel_id`.

## Notes

- Webex polling is used for inbound messages right now
- File upload delivery is not implemented yet
- Group spaces are blocked by design for now
