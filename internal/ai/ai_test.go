package ai

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Unscheduled-Maintenance/Holocron/internal/config"
	"github.com/Unscheduled-Maintenance/Holocron/internal/journal"
	"github.com/Unscheduled-Maintenance/Holocron/internal/report"
)

func sampleReport() report.Report {
	at := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	e1 := journal.Entry{ID: 3, Body: "Enabled IAM Access Analyzer", OccurredAt: at, Project: "AWS", Type: journal.TypeAccomplishment}
	e2 := journal.Entry{ID: 9, Body: "Decision: keep deploy model\nBecause of budget.", OccurredAt: at, Type: journal.TypeDecision}
	return report.Report{
		Kind: report.Staff, Title: "Staff update — this week",
		Sections: []report.Section{
			{Key: "completed", Title: "Completed", Items: []report.Item{{Text: e1.Title(), EntryIDs: []int64{3}}}},
			{Key: "decisions", Title: "Decisions", Items: []report.Item{{Text: e2.Title(), EntryIDs: []int64{9}}}},
		},
		Entries: map[int64]journal.Entry{3: e1, 9: e2},
	}
}

func TestBuildRequestContainsOnlySelectedEntries(t *testing.T) {
	req := BuildRequest(sampleReport())
	for _, want := range []string{`<entry id="#3"`, `<entry id="#9"`, "Because of budget.", "Staff update"} {
		if !strings.Contains(req.Prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	if strings.Count(req.Prompt, "<entry ") != 2 {
		t.Error("prompt should contain exactly the selected entries")
	}
	if !strings.Contains(req.System, "[#ID]") {
		t.Error("system prompt must ask for citations")
	}
}

func TestCitationsAndProvenance(t *testing.T) {
	rep := sampleReport()
	text := "# Update\n- Analyzer on everywhere [#3]\n- Something invented [#42] [#3]"
	cited, unknown := Citations(text, rep)
	if len(cited) != 1 || cited[0] != 3 || len(unknown) != 1 || unknown[0] != "#42" {
		t.Fatalf("cited=%v unknown=%v", cited, unknown)
	}
	p := Provenance(text, rep, "fake (model)")
	for _, want := range []string{"## Sources", "- [x] #3 Enabled IAM Access Analyzer", "- [ ] #9 Decision", "#42", "Review before sharing"} {
		if !strings.Contains(p, want) {
			t.Errorf("provenance missing %q:\n%s", want, p)
		}
	}
}

func TestNewProvider(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if _, err := New(config.AIConfig{}, env(nil)); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("empty provider: %v", err)
	}
	if _, err := New(config.AIConfig{Provider: "anthropic"}, env(nil)); err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Fatalf("missing key: %v", err)
	}
	p, err := New(config.AIConfig{Provider: "anthropic", APIKeyEnv: "MY_KEY"}, env(map[string]string{"MY_KEY": "sk-test"}))
	if err != nil || p.Name() != "anthropic (claude-opus-5-5)" {
		t.Fatalf("provider = %v, %v", p, err)
	}
	if strings.Contains(p.Name(), "sk-test") {
		t.Fatal("provider name must never include the key")
	}
	if _, err := New(config.AIConfig{Provider: "skynet"}, env(nil)); err == nil {
		t.Fatal("unknown provider accepted")
	}
}

func TestCitationsWithLabels(t *testing.T) {
	rep := sampleReport()
	e := rep.Entries[3]
	e.Num, e.Label = 12, "a"
	rep.Entries[3] = e
	cited, unknown := Citations("Done [#12a] and [#12b] [#3]", rep)
	if len(cited) != 1 || cited[0] != 3 || !reflect.DeepEqual(unknown, []string{"#12b", "#3"}) {
		t.Fatalf("cited=%v unknown=%v", cited, unknown)
	}
	if req := BuildRequest(rep); !strings.Contains(req.Prompt, `<entry id="#12a"`) {
		t.Error("prompt should identify entries by reference")
	}
	if p := Provenance("Done [#12a]", rep, "fake"); !strings.Contains(p, "- [x] #12a Enabled") {
		t.Errorf("provenance:\n%s", p)
	}
}
