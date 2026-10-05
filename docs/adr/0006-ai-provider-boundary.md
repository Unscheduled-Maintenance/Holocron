# 6. AI behind a provider interface, fed only selected entries

Date: 2026-10-05

## Status

Accepted

## Context

AI can turn a terse deterministic report into better prose, but Holocron's
records are often confidential, core features must never depend on a network
service, and every report sentence should stay traceable to entries.

## Decision

- `internal/ai.Provider` is a one-method interface (`Generate`). The only
  implementation is Anthropic's Messages API via the official Go SDK (default
  model `claude-opus-5-5`, medium effort, server-side refusal fallback).
  Nothing outside `internal/ai` imports a vendor SDK.
- The model never searches the archive. Holocron builds the deterministic
  report first and sends only the entries it selected, labelled with their
  IDs, asking for `[#ID]` citations.
- Output is post-processed: citations are checked against the supplied IDs,
  and a Sources appendix lists every supplied entry, whether it was cited,
  and any citation of an unknown ID.
- Sending needs explicit consent each time: `--ai` plus an interactive
  confirmation (or `--yes`) stating how many entries, which IDs and which
  provider.
- API keys come only from an environment variable named in the config
  (default `ANTHROPIC_API_KEY`), never from the config file or the archive.
- Any provider failure or refusal falls back to printing the deterministic
  report, with a non-zero exit status.

## Consequences

- Adding another provider (for example a local model server) means one new
  file implementing `Provider` and a config value.
- Reports stay reviewable: a reader can check each claim against its entry.
- Data sent externally is limited to the report's selection and disclosed
  before sending.
