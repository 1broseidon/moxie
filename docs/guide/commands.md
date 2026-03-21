# Chat Commands

Commands available in Telegram and Slack conversations. All commands start with `/`.

## Reference

| Command | Description |
|---------|-------------|
| `/new [backend] [workspace]` | Start a new conversation thread |
| `/model [backend] [model]` | Show or switch the agent backend |
| `/think [off\|low\|medium\|high]` | Show or set thinking/reasoning effort |
| `/cwd [name\|path]` | Show or switch working directory |
| `/threads [name]` | List or switch threads |
| `/compact` | Compact the current thread history |

## `/new`

Start a fresh conversation thread with an auto-generated name. You can optionally pass a backend or workspace name to switch them at the same time.

```
/new                    → new thread, current backend and cwd stay the same
/new codex              → new thread, switch backend to Codex
/new myapp              → new thread, switch working directory to 'myapp' workspace
/new codex myapp        → new thread, switch both
```

## `/model`

Switch the agent backend or model.

```
/model                  → show current backend and model
/model codex            → switch to Codex (default model)
/model claude opus      → switch to Claude with Opus
/model gemini           → switch to Gemini
```

If the argument matches a known backend name, the backend switches. Otherwise it's treated as a model name for the current backend.

## `/think`

Control reasoning effort for backends that support it (Claude, Codex, Pi).

```
/think                  → show current level
/think high             → extended thinking
/think medium           → balanced
/think low              → fast
/think off              → disable (default)
```

## `/cwd`

Switch the working directory the agent operates in.

```
/cwd                    → show current directory
/cwd /home/user/project → switch to absolute path
/cwd myapp              → switch to named workspace
/cwd myapp /path/to/it  → create a named workspace and switch to it
```

Named workspaces are saved to the config and persist across restarts.

## `/threads`

Manage conversation threads. Threads allow parallel conversations with different contexts.

```
/threads                → list all threads, current thread marked with >
/threads my-feature     → switch to or create thread "my-feature"
```

## `/compact`

Compact the current thread's history to reduce context size. Useful after long conversations.

```
/compact                → compact current thread
```
