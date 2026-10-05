package tui

import (
	"io"
	"os"
	"os/exec"
	"runtime"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/term"
)

// Windows consoles deliver the Esc key as a bare ESC byte, which is also the
// first byte of every arrow-key and function-key sequence. Bubble Tea must
// therefore wait 50 ms after each ESC to see whether more bytes follow, so
// Esc — Holocron's "back" key — felt noticeably slower than every other key
// (measured at 55–80 ms against 10–25 ms for other keys).
//
// Windows Terminal supports win32-input-mode (DECSET 9001), in which every
// key arrives as one complete, unambiguous sequence, and Bubble Tea's input
// decoder understands it, including bracketed paste and characters typed
// through IMEs or the emoji panel. Holocron enables it while an interactive
// program runs and always disables it afterwards, and while an external
// editor has the terminal.
//
// It is enabled only inside Windows Terminal (WT_SESSION is set). Hosts that
// feed the console plain UTF-8 text, such as VS Code's terminal, make the
// console synthesise Alt+Numpad key sequences for characters outside the
// keyboard layout, which Bubble Tea would mis-decode as digits; there the
// previous behaviour is kept. Setting HOLOCRON_LEGACY_INPUT=1 turns it off
// everywhere.

const (
	enableWin32Input  = "\x1b[?9001h"
	disableWin32Input = "\x1b[?9001l"
)

func win32InputWanted() bool {
	return runtime.GOOS == "windows" &&
		os.Getenv("WT_SESSION") != "" &&
		os.Getenv("HOLOCRON_LEGACY_INPUT") == "" &&
		term.IsTerminal(int(os.Stdout.Fd())) &&
		term.IsTerminal(int(os.Stdin.Fd()))
}

// withUnambiguousKeys runs an interactive program with win32-input-mode
// enabled where supported.
func withUnambiguousKeys(run func() error) error {
	if !win32InputWanted() {
		return run()
	}
	_, _ = io.WriteString(os.Stdout, enableWin32Input)
	defer func() { _, _ = io.WriteString(os.Stdout, disableWin32Input) }()
	return run()
}

// plainInputExec runs an external program (the person's editor) with the
// terminal back in its normal input mode, restoring win32-input-mode after.
type plainInputExec struct{ cmd *exec.Cmd }

func (p plainInputExec) Run() error {
	if !win32InputWanted() {
		return p.cmd.Run()
	}
	_, _ = io.WriteString(os.Stdout, disableWin32Input)
	defer func() { _, _ = io.WriteString(os.Stdout, enableWin32Input) }()
	return p.cmd.Run()
}

func (p plainInputExec) SetStdin(r io.Reader) {
	if p.cmd.Stdin == nil {
		p.cmd.Stdin = r
	}
}

func (p plainInputExec) SetStdout(w io.Writer) {
	if p.cmd.Stdout == nil {
		p.cmd.Stdout = w
	}
}

func (p plainInputExec) SetStderr(w io.Writer) {
	if p.cmd.Stderr == nil {
		p.cmd.Stderr = w
	}
}

// execEditor hands the terminal to an editor process.
func execEditor(cmd *exec.Cmd, fn tea.ExecCallback) tea.Cmd {
	return tea.Exec(plainInputExec{cmd: cmd}, fn)
}
