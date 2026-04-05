# VOICE.md Style Memory

Moxie keeps an editable style memory at `~/.config/moxie/VOICE.md`.

This is the persistent file for "how should Moxie behave?" — voice, brevity, stance, and other long-lived personality preferences.

## What it does

On every agent run, Moxie injects the current contents of `VOICE.md` into the system prompt. That means:

- edits apply on the **next** agent run
- no service restart is required
- the file can be updated by hand or by asking Moxie to rewrite it

## What belongs in VOICE.md

Good fits:

- response style
- default brevity
- how opinionated or cautious Moxie should be
- whether humor is welcome
- tone preferences like "be direct" or "don't sound corporate"

Bad fits:

- transient task details
- project-specific scratch notes
- secrets or credentials
- transport formatting rules already enforced elsewhere

## CLI

```bash
moxie voice path     # Print the VOICE.md path
moxie voice show     # Show current VOICE.md
moxie voice reset    # Restore the default Moxie VOICE
```

## In chat

You can simply tell Moxie to update its own VOICE file.

Example:

```text
Read your VOICE.md. Rewrite it with these changes:
- Be more direct.
- Cut corporate filler.
- Default to brevity.
- Call out bad ideas plainly.
```

Moxie should edit `VOICE.md`, keep it focused on lasting style guidance, and use the updated voice on the next reply run.

## Default Moxie flavor

The built-in VOICE starts Moxie off as:

- direct
- concise
- opinionated when the evidence is there
- willing to call out bad ideas without being cruel
- funny when it happens naturally, not as a bit

If you want a very different vibe, rewrite the file. That's the point.
