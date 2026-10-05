package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Unscheduled-Maintenance/Holocron/internal/ai"
	"github.com/Unscheduled-Maintenance/Holocron/internal/config"
)

type fakeProvider struct {
	got  ai.Request
	text string
	err  error
}

func (f *fakeProvider) Name() string { return "fake (test-model)" }
func (f *fakeProvider) Generate(_ context.Context, r ai.Request) (ai.Response, error) {
	f.got = r
	return ai.Response{Text: f.text}, f.err
}

func withProvider(t *testing.T, p ai.Provider, err error) {
	t.Helper()
	old := newAIProvider
	newAIProvider = func(config.AIConfig) (ai.Provider, error) { return p, err }
	t.Cleanup(func() { newAIProvider = old })
}

func TestAIReport(t *testing.T) {
	h := newHarness(t)
	h.ok("add", "Enabled IAM Access Analyzer", "-p", "aws", "--type", "accomplishment")
	h.ok("add", "Private note that must not be sent", "--type", "note")

	withProvider(t, nil, ai.ErrNotConfigured)
	if r := h.run("report", "staff", "--ai", "--yes"); r.code == 0 || !strings.Contains(r.err, "not configured") {
		t.Fatalf("unconfigured: %+v", r)
	}

	fp := &fakeProvider{text: "# Staff update\n- Analyzer enabled in every region [#1]"}
	withProvider(t, fp, nil)
	r := h.run("report", "staff", "--ai")
	if r.code != ExitUsage || !strings.Contains(r.err, "--yes") || fp.got.Prompt != "" {
		t.Fatalf("without consent nothing may be sent: %+v", r)
	}
	out := h.ok("report", "staff", "--ai", "--yes")
	mustContain(t, out, "Analyzer enabled in every region [#1]", "## Sources", "- [x] #1", "fake (test-model)")
	if strings.Contains(fp.got.Prompt, "Private note") {
		t.Fatal("an entry the report did not select was sent to the provider")
	}

	file := filepath.Join(h.dir, "ai.md")
	h.ok("report", "staff", "--ai", "--yes", "-o", file)
	if b, _ := os.ReadFile(file); !strings.Contains(string(b), "## Sources") {
		t.Fatal("AI report file missing provenance")
	}

	withProvider(t, &fakeProvider{err: errors.New("network down")}, nil)
	r = h.run("report", "staff", "--ai", "--yes")
	if r.code == 0 || !strings.Contains(r.out, "Enabled IAM Access Analyzer") || !strings.Contains(r.err, "deterministic report instead") {
		t.Fatalf("AI failure should fall back to the deterministic report: %+v", r)
	}
}
