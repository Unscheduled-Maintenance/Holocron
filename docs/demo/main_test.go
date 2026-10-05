package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAssembleOrdersSettingsBeforeCommands(t *testing.T) {
	dir := t.TempDir()
	tape := filepath.Join(dir, "x.tape")
	content := "# comment\nOutput docs/images/x.gif\nSet Height 440\nType \"holocron today\"\nEnter\n"
	if err := os.WriteFile(tape, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := assemble(tape, "Set FontSize 16\nSet Height 820", dir)
	if err != nil {
		t.Fatal(err)
	}
	order := []string{"Output docs/images/x.gif", "Set Shell", "Set FontSize 16", "Set Height 820", "Set Height 440", "Hide", "Show", `Type "holocron today"`}
	pos := -1
	for _, s := range order {
		i := strings.Index(got, s)
		if i < 0 || i < pos {
			t.Fatalf("%q missing or out of order in:\n%s", s, got)
		}
		pos = i
	}

	// A screenshot-only tape gets a scratch GIF output with a quoted path.
	if err := os.WriteFile(tape, []byte("Type \"x\"\nScreenshot docs/images/x.png\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = assemble(tape, "", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, `Output "`) || !strings.Contains(got, `discard.gif"`) {
		t.Fatalf("expected a quoted scratch output, got:\n%s", got)
	}
}
