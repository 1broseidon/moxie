# Backends

Moxie dispatches messages to supported agent CLIs like Claude, Codex, Gemini, Pi, and OpenCode.

## Supported backends

| Backend | CLI | Install | Notes |
|---------|-----|---------|-------|
| Claude | `claude` | `npm install -g @anthropic-ai/claude-code` | Default backend |
| Codex | `codex` | `npm install -g @openai/codex` | Sandboxed execution |
| Gemini | `gemini` | `npm install -g @google/gemini-cli` | Requires Google auth |
| Pi | `pi` | `npm install -g @anthropics/pi` | Multi-model routing |
| OpenCode | `opencode` | See [opencode.ai](https://opencode.ai) | |

After installing a backend CLI, make sure it is on your `PATH`:

```bash
command -v claude
command -v codex
command -v gemini
command -v pi
command -v opencode
```

## Switching backends

In chat, use `/model` to switch:

```
/model                  → show current backend and model
/model codex            → switch to Codex
/model gemini           → switch to Gemini
/model claude sonnet    → switch to Claude with Sonnet model
/model pi grok3         → switch to Pi with Grok 3
```

The backend and model are persisted per conversation. Telegram and Slack maintain separate state.

## Thinking levels

For backends that support reasoning effort control (Claude, Codex, Pi), use `/think`:

```
/think          → show current level
/think high     → extended thinking
/think medium   → balanced
/think low      → fast responses
/think off      → disable (default)
```

When thinking is enabled, the `--effort` or `--thinking` flag is passed to the backend CLI. When disabled, the flag is omitted entirely.

## Working directory

The agent runs in a working directory you control:

```bash
# Set at startup
moxie serve --cwd /home/user/projects/myapp
```

Or switch in chat to a named workspace:

```
/cwd myapp
```

### Named workspaces

Pre-configure directories in your config:

```json
{
  "workspaces": {
    "myapp": "/home/user/projects/myapp",
    "ops": "/home/user/projects/ops"
  }
}
```

Then switch by name: `/cwd myapp`

## Custom backend config

Moxie loads built-in defaults for supported backends and applies overrides from `~/.config/moxie/backends.json`. To override settings or add new backends, create:

```json
{
  "claude": {
    "model": "opus"
  },
  "my-custom-agent": {
    "run": "my-agent --prompt {prompt} --json",
    "format": "jsonl",
    "result": "text",
    "result_when": "type=done"
  }
}
```

User overrides are merged on top of embedded defaults.
