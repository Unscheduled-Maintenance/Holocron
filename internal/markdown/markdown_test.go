package markdown

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestRender(t *testing.T) {
	cases := []struct {
		in        string
		want      []string // substrings of the plain rendering
		forbidden []string
	}{
		{"Use _markdown_ and **bold**", []string{"Use markdown and bold"}, []string{"**", "_markdown_"}},
		{"Fixed <script> handling for Vec<T>", []string{"Fixed <script> handling for Vec<T>"}, []string{`\<`}},
		{"Inline `Vec<T>` code", []string{"Vec<T>"}, []string{`\<`}},
		{"```\nif a < b {}\n```", []string{"if a < b {}"}, []string{`\<`}},
		{"first line\nsecond line", []string{"first line\nsecond line"}, nil},
		{"#security is a tag, not a heading", []string{"#security is a tag"}, nil},
		{"- one\n- two", []string{"• one", "• two"}, nil},
	}
	for _, c := range cases {
		for _, dark := range []bool{true, false} {
			out := Render(c.in, 60, dark)
			plain := ansi.Strip(out)
			for _, w := range c.want {
				if !strings.Contains(plain, w) {
					t.Errorf("Render(%q) = %q, missing %q", c.in, plain, w)
				}
			}
			for _, f := range c.forbidden {
				if strings.Contains(plain, f) {
					t.Errorf("Render(%q) = %q, should not contain %q", c.in, plain, f)
				}
			}
			if strings.HasPrefix(plain, "\n") || strings.HasSuffix(plain, "\n") || strings.Contains(plain, " \n") {
				t.Errorf("Render(%q) has padding: %q", c.in, plain)
			}
		}
	}
}

func TestRenderWraps(t *testing.T) {
	long := strings.Repeat("word ", 40)
	for _, l := range strings.Split(Render(long, 30, true), "\n") {
		if w := lipgloss.Width(l); w > 30 {
			t.Fatalf("line %q is %d cells wide", l, w)
		}
	}
}

func TestRenderHighlightedCodeBlock(t *testing.T) {
	in := "Before\n\n```go\nfunc main() { fmt.Println(\"hi\") }\n```\n\nAfter"
	for _, dark := range []bool{true, false} {
		out := Render(in, 60, dark)
		plain := ansi.Strip(out)
		for _, want := range []string{"Before", `func main() { fmt.Println("hi") }`, "After"} {
			if !strings.Contains(plain, want) {
				t.Errorf("dark=%v: missing %q in %q", dark, want, plain)
			}
		}
		if strings.Contains(plain, "```") {
			t.Errorf("dark=%v: fence markers should not be shown: %q", dark, plain)
		}
		if out == plain {
			t.Errorf("dark=%v: code block was not styled", dark)
		}
	}
}
