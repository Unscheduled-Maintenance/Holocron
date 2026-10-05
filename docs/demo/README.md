# README images

The GIFs and screenshots in `docs/images` are recorded with
[VHS](https://github.com/charmbracelet/vhs) from the tapes in
`docs/demo/tapes`. They are reproducible: one command rebuilds Holocron,
seeds a throwaway archive and re-records everything.

```bash
go run ./docs/demo              # every tape
go run ./docs/demo tui search   # only some tapes
```

Your real archive is never touched. Each tape gets its own freshly seeded
archive in a temporary directory, with entries dated relative to today so
the recordings always show recent activity.

## Requirements

| Tool | Install |
|---|---|
| VHS **v0.11.0** | `go install github.com/charmbracelet/vhs@v0.11.0` |
| ttyd | `winget install tsl0922.ttyd`, `brew install ttyd`, `apt install ttyd` |
| ffmpeg | `winget install Gyan.FFmpeg`, `brew install ffmpeg`, `apt install ffmpeg` |
| Chrome or Chromium | VHS drives it headlessly |

VHS is pinned to v0.11.0 because v0.12.0 and v0.12.1 cancel their own context
just before encoding, so they exit successfully without writing any image.
`go run ./docs/demo` refuses those versions rather than silently producing
nothing.

The images can also be regenerated in CI: run the **README images**
workflow (`.github/workflows/demo.yml`) and download its `readme-images`
artifact.

## How it fits together

- `docs/demo/main.go` builds Holocron, seeds archives, assembles each tape
  and runs VHS from the repository root.
- `docs/demo/style.tape` is the shared look. It is prepended to every tape
  together with the shell and a hidden prompt set-up, so individual tapes
  contain only their output paths, any size overrides (`Set Height …`) and
  the actions to record.
- `docs/demo/tapes/*.tape` are the recordings. A tape with no `Output` line
  only takes screenshots.

Recordings use PowerShell on Windows and Bash elsewhere, without any
profile or rc files. In PowerShell, history-based suggestions are turned off
and in-memory history is cleared before recording, so nothing from your own
shell history can appear in an image.

## Theme

The look is derived from Holocron's own palette (`internal/style`):

- the terminal background is a near-black teal (`#0A1416`) and the
  cursor and ANSI cyan use the archive accent (`#00AFAF`, ANSI 44);
- yellow is the amber used for highlights, and red, green, blue and magenta
  match the entry-type colours;
- the window sits on a deep-teal backfill (`Margin` + `MarginFill #0B3438`)
  with generous padding, rounded corners and a quiet ring-style window bar;
- the prompt is a cyan `›`.

Fonts: JetBrains Mono where installed (CI installs it), otherwise Cascadia
Mono, DejaVu Sans Mono or Consolas. Recordings made on different machines
can therefore differ slightly in typeface; within one run they are
consistent.

## Adding a recording

1. Create `docs/demo/tapes/<name>.tape` with an `Output docs/images/<name>.gif`
   line and/or `Screenshot docs/images/<name>.png` commands.
2. Run `go run ./docs/demo <name>` and check the result.
3. Reference the image from the README.
