# 4. One application layer shared by CLI and TUI

Date: 2026-10-05

## Status

Accepted

## Context

Holocron is "CLI first, TUI enhanced": everything important must be possible
from the command line, and the TUI must not grow a parallel implementation or
TUI-only features.

## Decision

- Behaviour lives in `internal/app` (workflows such as capture, the editor
  document, backup, restore and doctor) and in the domain packages
  (`journal`, `report`, `export`, `timerange`, `gitimport`, `ai`).
- `internal/cli` (Cobra) and `internal/tui` (Bubble Tea) only translate input
  into calls on those packages and render the results. Neither issues SQL.
- Capture shorthand, the editor document format, report selection and date
  parsing each have exactly one implementation, used by both.
- The TUI performs I/O in `tea.Cmd` functions so that `Update` stays a state
  transition tests can drive without a terminal.
- No interface is introduced between presentation and `journal.Store`; tests
  use real temporary SQLite archives instead of mocks.

## Consequences

- A feature added to the TUI is cheap to expose in the CLI and vice versa.
- Behavioural tests sit at the domain and CLI levels; TUI tests concentrate
  on navigation, focus, escape semantics and layout at many terminal sizes.
- The application layer stays free of presentation concerns (colour,
  terminal width), which belong to `internal/style` and the renderers.
