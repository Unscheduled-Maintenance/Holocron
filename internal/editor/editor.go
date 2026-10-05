// Package editor opens the person's own text editor on a temporary file.
// Holocron never implements an editor of its own.
package editor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// Resolve returns the editor command line to use, in priority order:
// the Holocron config "editor" setting, $VISUAL, $EDITOR, then a platform
// fallback (notepad on Windows; editor, nano or vi elsewhere).
func Resolve(configured string) ([]string, string, error) {
	for _, c := range []struct{ value, source string }{
		{configured, "config editor"},
		{os.Getenv("VISUAL"), "$VISUAL"},
		{os.Getenv("EDITOR"), "$EDITOR"},
	} {
		if strings.TrimSpace(c.value) == "" {
			continue
		}
		argv := SplitCommand(c.value)
		if len(argv) == 0 {
			continue
		}
		return argv, c.source, nil
	}
	fallbacks := []string{"editor", "nano", "vi"}
	if runtime.GOOS == "windows" {
		fallbacks = []string{"notepad"}
	}
	for _, f := range fallbacks {
		if p, err := exec.LookPath(f); err == nil {
			return []string{p}, "default", nil
		}
	}
	return nil, "", errors.New("no editor found; set $EDITOR (for example `code --wait`, `nano` or `notepad`) or `editor` in the Holocron config")
}

// SplitCommand splits a command line into arguments, honouring single and
// double quotes. Backslashes are literal so Windows paths work unquoted.
func SplitCommand(s string) []string {
	var args []string
	var cur strings.Builder
	inArg := false
	var quote rune
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote = r
			inArg = true
		case r == ' ' || r == '\t':
			if inArg {
				args = append(args, cur.String())
				cur.Reset()
				inArg = false
			}
		default:
			cur.WriteRune(r)
			inArg = true
		}
	}
	if inArg {
		args = append(args, cur.String())
	}
	return args
}

// Command builds the exec.Cmd that edits file. Callers attach stdio (the CLI)
// or hand it to Bubble Tea's ExecProcess (the TUI).
func Command(ctx context.Context, argv []string, file string) *exec.Cmd {
	args := append(append([]string{}, argv[1:]...), file)
	return exec.CommandContext(ctx, argv[0], args...)
}

// TempFile creates a private temporary file holding initial content.
func TempFile(initial string) (string, error) {
	f, err := os.CreateTemp("", "holocron-*.md")
	if err != nil {
		return "", fmt.Errorf("creating a temporary file for the editor: %w", err)
	}
	if _, err := f.WriteString(initial); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// Edit opens the editor on initial text attached to the current terminal
// and returns the saved text.
func Edit(ctx context.Context, argv []string, initial string) (string, error) {
	path, err := TempFile(initial)
	if err != nil {
		return "", err
	}
	defer os.Remove(path)
	cmd := Command(ctx, argv, path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("editor %q failed: %w", strings.Join(argv, " "), err)
	}
	return ReadBack(path)
}

// ReadBack reads an edited temporary file.
func ReadBack(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading the edited file: %w", err)
	}
	s := strings.TrimPrefix(string(b), "\ufeff") // Notepad may add a BOM
	return strings.ReplaceAll(s, "\r\n", "\n"), nil
}
