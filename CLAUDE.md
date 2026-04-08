# Moxie

Chat agent service connecting Telegram, Slack, and Webex to AI coding backends (Claude, Codex, Gemini, Pi). Go 1.24+, single binary, runs as a systemd service.

## Code Exploration Policy
Use `cymbal` CLI for code navigation — prefer it over Read, Grep, Glob, or Bash for code exploration.
- **New to a repo?**: `cymbal structure` — entry points, hotspots, central packages. Start here.
- **To understand a symbol**: `cymbal investigate <symbol>` — returns source, callers, impact, or members based on what the symbol is.
- **To understand multiple symbols**: `cymbal investigate Foo Bar Baz` — batch mode, one invocation.
- **To trace an execution path**: `cymbal trace <symbol>` — follows the call graph downward (what does X call, what do those call).
- **To assess change risk**: `cymbal impact <symbol>` — follows the call graph upward (what breaks if X changes).
- Before reading a file: `cymbal outline <file>` or `cymbal show <file:L1-L2>`
- Before searching: `cymbal search <query>` (symbols) or `cymbal search <query> --text` (grep)
- Before exploring structure: `cymbal ls` (tree) or `cymbal ls --stats` (overview)
- To disambiguate: `cymbal show path/to/file.go:SymbolName` or `cymbal investigate file.go:Symbol`
- The index auto-builds on first use — no manual indexing step needed. Queries auto-refresh incrementally.
- All commands support `--json` for structured output.
- **NEVER run cymbal commands in the background** (`run_in_background`). Always run foreground. Background task notifications interleave with user messages and cause dropped responses.

## Project layout
- `cmd/moxie/` — CLI entry point
- `internal/` — all packages (dispatch, prompt, config, transport, store, memory, etc.)
- `scripts/` — helper scripts
- `docs/` — documentation

## Build & test
```bash
make build   # CGO_ENABLED=1, -tags fts5
make test    # runs full suite
```

## Service management
```bash
moxie service restart   # restart the running service
# Never run "moxie serve" directly — it creates a duplicate process
```

## Conventions
- Keep the single-binary, zero-infra philosophy. No external databases unless embedded.
- Tests before trust. Add tests for new behavior.
- Error handling: return errors, don't panic. Memory/optional features must not block core dispatch.
