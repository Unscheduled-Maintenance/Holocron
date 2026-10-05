package editor

import (
	"reflect"
	"testing"
)

func TestSplitCommand(t *testing.T) {
	cases := map[string][]string{
		"vim":                                 {"vim"},
		"code --wait":                         {"code", "--wait"},
		`"C:\Program Files\Editor\ed.exe" -w`: {`C:\Program Files\Editor\ed.exe`, "-w"},
		`subl -n -w`:                          {"subl", "-n", "-w"},
		`emacsclient -a '' -t`:                {"emacsclient", "-a", "", "-t"},
		"   ":                                 nil,
	}
	for in, want := range cases {
		if got := SplitCommand(in); !reflect.DeepEqual(got, want) {
			t.Errorf("SplitCommand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolvePriority(t *testing.T) {
	t.Setenv("VISUAL", "visual-editor")
	t.Setenv("EDITOR", "plain-editor")
	if argv, src, _ := Resolve("configured --flag"); argv[0] != "configured" || src != "config editor" {
		t.Fatalf("config should win: %v %s", argv, src)
	}
	if argv, _, _ := Resolve(""); argv[0] != "visual-editor" {
		t.Fatalf("$VISUAL should beat $EDITOR: %v", argv)
	}
	t.Setenv("VISUAL", "")
	if argv, _, _ := Resolve(""); argv[0] != "plain-editor" {
		t.Fatalf("$EDITOR fallback: %v", argv)
	}
}
