package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Unscheduled-Maintenance/Holocron/internal/config"
)

// harness runs the CLI in-process against an isolated archive.
type harness struct {
	t   *testing.T
	dir string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(config.EnvDB, "")
	t.Setenv(config.EnvData, filepath.Join(dir, "data"))
	t.Setenv(config.EnvConfig, filepath.Join(dir, "config.toml"))
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NO_COLOR", "")
	return &harness{t: t, dir: dir}
}

type result struct {
	out, err string
	code     int
}

func (h *harness) runIn(stdin string, args ...string) result {
	h.t.Helper()
	var out, errb bytes.Buffer
	code := Execute(context.Background(), args, IO{In: strings.NewReader(stdin), Out: &out, Err: &errb})
	return result{out.String(), errb.String(), code}
}

func (h *harness) run(args ...string) result {
	h.t.Helper()
	return h.runIn("", args...)
}

func (h *harness) ok(args ...string) string {
	h.t.Helper()
	r := h.run(args...)
	if r.code != 0 {
		h.t.Fatalf("holocron %s: exit %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), r.code, r.out, r.err)
	}
	return r.out
}

func mustContain(t *testing.T, s string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(s, w) {
			t.Errorf("output missing %q:\n%s", w, s)
		}
	}
}

func mustNotContain(t *testing.T, s string, unwanted ...string) {
	t.Helper()
	for _, w := range unwanted {
		if strings.Contains(s, w) {
			t.Errorf("output should not contain %q:\n%s", w, s)
		}
	}
}

// TestDefinitionOfDone walks the core capture-and-retrieve workflow.
func TestDefinitionOfDone(t *testing.T) {
	h := newHarness(t)
	out := h.ok("add", "Investigated an S3 policy issue", "--project", "aws", "--type", "investigation", "--tag", "security")
	mustContain(t, out, "Added", "#1", "aws", "Investigated an S3 policy issue")
	h.ok("add", "Unrelated note about lunch")

	mustContain(t, h.ok("today"), "Investigated an S3 policy issue", "Unrelated note")
	s3 := h.ok("search", "s3")
	mustContain(t, s3, "Investigated an S3 policy issue")
	mustNotContain(t, s3, "lunch")
	mustContain(t, h.ok("search", "--project", "aws"), "S3 policy")
	mustNotContain(t, h.ok("search", "--project", "aws"), "lunch")
	mustContain(t, h.ok("search", "--tag", "security"), "S3 policy")
	mustNotContain(t, h.ok("search", "--tag", "security"), "lunch")
	mustContain(t, h.ok("search", "policy +aws #security"), "S3 policy")
}

func TestCaptureForms(t *testing.T) {
	h := newHarness(t)
	h.ok("add", "Fixed S3 bucket policy +aws #security #iam")
	r := h.runIn("Fixed the deployment issue\n", "add")
	if r.code != 0 {
		t.Fatalf("stdin add failed: %+v", r)
	}
	h.ok("add", "--at", "yesterday 16:00", "Reviewed repository moderation queue", "-p", "CCR")
	h.ok("add", "Decision: retain the existing deployment model for now", "-p", "infra", "--mark", "staff")
	raw := h.ok("add", "--raw", "Keep +literal #text", "--json")

	var entry struct {
		ID      int64    `json:"id"`
		Body    string   `json:"body"`
		Project *string  `json:"project"`
		Tags    []string `json:"tags"`
	}
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		t.Fatalf("--json output: %v\n%s", err, raw)
	}
	if entry.Body != "Keep +literal #text" || entry.Project != nil || len(entry.Tags) != 0 {
		t.Fatalf("--raw entry = %+v", entry)
	}

	var all []struct {
		ID      int64    `json:"id"`
		Body    string   `json:"body"`
		Project *string  `json:"project"`
		Type    *string  `json:"type"`
		Tags    []string `json:"tags"`
		Marks   []string `json:"marks"`
	}
	if err := json.Unmarshal([]byte(h.ok("list", "--json", "--limit", "0", "--reverse")), &all); err != nil {
		t.Fatal(err)
	}
	if len(all) != 5 {
		t.Fatalf("expected 5 entries, got %d", len(all))
	}
	byBody := map[string]int{}
	for i, e := range all {
		byBody[e.Body] = i
	}
	fixed := all[byBody["Fixed S3 bucket policy"]]
	if fixed.Project == nil || *fixed.Project != "aws" || strings.Join(fixed.Tags, ",") != "iam,security" {
		t.Errorf("shorthand not applied: %+v", fixed)
	}
	dec := all[byBody["retain the existing deployment model for now"]]
	if dec.Type == nil || *dec.Type != "decision" || len(dec.Marks) != 1 {
		t.Errorf("type inference or mark missing: %+v", dec)
	}
	if _, ok := byBody["Fixed the deployment issue"]; !ok {
		t.Error("stdin entry missing")
	}
	mustContain(t, h.ok("yesterday"), "Reviewed repository moderation queue")

	// Invalid input fails with a usage exit code and saves nothing.
	for _, args := range [][]string{
		{"add"},
		{"add", "x", "--type", "chore"},
		{"add", "x", "--at", "teatime"},
		{"add", "+aws +gcp moved"},
		{"add", "x", "--mark", "lunch"},
	} {
		if r := h.run(args...); r.code != ExitUsage {
			t.Errorf("holocron %v: exit %d, want %d (%s)", args, r.code, ExitUsage, r.err)
		}
	}
	if n := strings.Count(h.ok("list", "--limit", "0"), "\n  "); n != 5 {
		t.Errorf("failed adds changed the archive: %d rows", n)
	}
}

func TestEditDeleteMarkResolve(t *testing.T) {
	h := newHarness(t)
	h.ok("add", "Exporter keeps restarting", "--type", "problem", "-p", "infra")
	h.ok("add", "Second entry")

	mustContain(t, h.ok("edit", "1", "--text", "Exporter restarts every night", "--tag", "monitoring", "--type", "prob"), "Updated", "#monitoring")
	mustContain(t, h.ok("show", "1"), "Exporter restarts every night", "infra", "problem", "open")
	if r := h.run("edit", "1"); r.code != ExitUsage {
		t.Errorf("edit without flags and without a terminal should be a usage error, got %d", r.code)
	}
	mustContain(t, h.ok("search", "nightly OR night"), "Exporter restarts")
	mustNotContain(t, h.ok("search", "keeps"), "Exporter")

	mustContain(t, h.ok("mark", "1", "staff", "1on1"), "◇one-on-one", "◇staff")
	mustContain(t, h.ok("list", "--mark", "staff"), "#1")
	mustContain(t, h.ok("unmark", "1", "all"), "Unmarked")
	mustNotContain(t, h.ok("list", "--mark", "staff"), "#1")

	mustContain(t, h.ok("list", "--open"), "#1")
	h.ok("resolve", "1")
	mustNotContain(t, h.ok("list", "--open"), "#1")
	h.ok("resolve", "1", "--reopen")
	mustContain(t, h.ok("list", "--open"), "#1")

	if r := h.run("delete", "2"); r.code != ExitUsage {
		t.Fatalf("delete without --yes and without a terminal: exit %d", r.code)
	}
	mustContain(t, h.ok("show", "2"), "Second entry")
	h.ok("delete", "2", "--yes")
	if r := h.run("show", "2"); r.code != ExitNotFound {
		t.Fatalf("show deleted: exit %d", r.code)
	}
	mustContain(t, h.ok("add", "Third entry"), "#3")
	if r := h.run("show", "nonsense"); r.code != ExitUsage {
		t.Fatalf("bad reference: exit %d", r.code)
	}
}

func TestProjectsAndTags(t *testing.T) {
	h := newHarness(t)
	h.ok("project", "add", "Chocolatey Infrastructure", "--alias", "infra", "--alias", "ops", "--description", "Servers")
	h.ok("add", "Patched servers", "-p", "ops")
	h.ok("add", "Rotated keys +infra #security")
	list := h.ok("project", "list")
	mustContain(t, list, "Chocolatey Infrastructure", "2 entries", "aka infra, ops")
	mustContain(t, h.ok("project", "show", "infra"), "Servers", "Patched servers", "Rotated keys")
	h.ok("project", "edit", "infra", "--name", "Infra Platform")
	mustContain(t, h.ok("search", "platform"), "Patched servers")
	h.ok("project", "archive", "ops")
	mustNotContain(t, h.ok("project", "list"), "Infra Platform")
	mustContain(t, h.ok("project", "list", "--all"), "archived")
	if r := h.run("project", "add", "OPS"); r.code == 0 {
		t.Error("creating a project named like an alias should fail")
	}
	mustContain(t, h.ok("tag", "list"), "#security")
	h.ok("tag", "rename", "security", "secops")
	mustContain(t, h.ok("search", "--tag", "secops"), "Rotated keys")
	if r := h.run("search", "--project", "missing"); r.code != ExitNotFound {
		t.Errorf("unknown project filter: exit %d", r.code)
	}
}

func TestReportsAndExport(t *testing.T) {
	h := newHarness(t)
	h.ok("add", "Enabled IAM Access Analyzer in all AWS regions", "-p", "aws", "--type", "accomplishment")
	h.ok("add", "Decision: retain the existing deployment model", "-p", "infra")
	h.ok("add", "Follow up with engineering about ARM build capacity", "--type", "follow-up", "--mark", "cross-team")
	h.ok("add", "Tidied desk", "--type", "note")

	staff := h.ok("report", "staff", "--ids", "--explain")
	mustContain(t, staff, "Staff update", "COMPLETED", "Enabled IAM Access Analyzer", "DECISIONS", "FOR OTHER TEAMS", "#1", "why:")
	mustNotContain(t, staff, "Tidied desk")

	md := h.ok("report", "staff", "--format", "markdown")
	mustContain(t, md, "# Staff update", "## Completed", "- **aws:** Enabled IAM Access Analyzer")
	var js map[string]any
	if err := json.Unmarshal([]byte(h.ok("report", "week", "--format", "json")), &js); err != nil {
		t.Fatalf("report json: %v", err)
	}
	if js["format"] != "holocron.report/v1" {
		t.Errorf("report format = %v", js["format"])
	}
	for _, k := range []string{"day", "week", "one-on-one", "quarter", "1on1"} {
		h.ok("report", k)
	}
	out := filepath.Join(h.dir, "reports", "staff.md")
	h.ok("report", "staff", "-f", "markdown", "-o", out)
	if b, err := os.ReadFile(out); err != nil || !strings.Contains(string(b), "# Staff update") {
		t.Fatalf("report file: %v %q", err, b)
	}
	if r := h.run("report", "staff", "--format", "pdf"); r.code != ExitUsage {
		t.Errorf("bad format: exit %d", r.code)
	}
	if r := h.run("report", "week", "--range", "today", "--since", "7d"); r.code != ExitUsage {
		t.Errorf("conflicting range flags: exit %d", r.code)
	}

	mdExport := h.ok("export", "--format", "markdown")
	mustContain(t, mdExport, "# Holocron archive", "Enabled IAM Access Analyzer", "`#1`", "project: aws")
	var doc struct {
		Format  string `json:"format"`
		Entries []struct {
			Body string `json:"body"`
		} `json:"entries"`
		Projects []struct {
			Name string `json:"name"`
		} `json:"projects"`
	}
	if err := json.Unmarshal([]byte(h.ok("export", "--format", "json", "--project", "aws")), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Format != "holocron.export/v1" || len(doc.Entries) != 1 || len(doc.Projects) != 1 {
		t.Fatalf("filtered export = %+v", doc)
	}
	file := filepath.Join(h.dir, "out", "all.json")
	h.ok("export", "-f", "json", "-o", file, "--since", "30d")
	if _, err := os.Stat(file); err != nil {
		t.Fatal(err)
	}
}

func TestBackupAndRestore(t *testing.T) {
	h := newHarness(t)
	h.ok("add", "Before backup one")
	h.ok("add", "Before backup two")
	dest := filepath.Join(h.dir, "copies", "b.db")
	mustContain(t, h.ok("backup", "-o", dest), "Backed up", "2 entries", "integrity check passed")
	if r := h.run("backup", "-o", dest); r.code == 0 {
		t.Error("backup overwrote an existing file")
	}
	h.ok("add", "After backup") // #3
	if r := h.run("restore", dest); r.code != ExitUsage {
		t.Fatalf("restore without --force and without a terminal: exit %d", r.code)
	}
	mustContain(t, h.ok("restore", dest, "--force"), "Restored 2 entries", "Previous archive saved")
	mustNotContain(t, h.ok("list"), "After backup")
	// #3 was issued before the restore and must never be reused.
	mustContain(t, h.ok("add", "After restore"), "#4")
	safety, _ := filepath.Glob(filepath.Join(h.dir, "data", "backups", "holocron-pre-restore-*.db"))
	if len(safety) != 1 {
		t.Fatalf("safety backups: %v", safety)
	}
	if r := h.run("restore", filepath.Join(h.dir, "missing.db"), "--force"); r.code == 0 {
		t.Error("restoring a missing file succeeded")
	}
	junk := filepath.Join(h.dir, "junk.db")
	_ = os.WriteFile(junk, bytes.Repeat([]byte("not sqlite "), 500), 0o600)
	if r := h.run("restore", junk, "--force"); r.code == 0 {
		t.Error("restoring a non-database succeeded")
	}
	mustContain(t, h.ok("list"), "After restore")
}

func TestPlainOutputForPipes(t *testing.T) {
	h := newHarness(t)
	h.ok("add", "Plain output check +aws #tag")
	for _, args := range [][]string{{"today"}, {"search", "plain"}, {"report", "week"}, {"show", "1"}, {"project", "list"}, {"doctor"}} {
		out := h.ok(args...)
		if strings.Contains(out, "\x1b[") {
			t.Errorf("%v wrote ANSI escapes to a non-terminal:\n%q", args, out)
		}
	}
	t.Setenv("NO_COLOR", "1")
	if out := h.ok("--color", "auto", "today"); strings.Contains(out, "\x1b[") {
		t.Error("NO_COLOR ignored")
	}
	if out := h.ok("--color", "always", "show", "1"); !strings.Contains(out, "\x1b[") {
		t.Error("--color always produced no colour")
	}
}

func TestMiscCommands(t *testing.T) {
	h := newHarness(t)
	mustContain(t, h.ok("version"), "holocron", "schema version")
	mustContain(t, h.ok("config", "path"), "config.toml", "holocron.db")
	mustContain(t, h.ok("config", "show"), "week_start")
	mustContain(t, h.ok("doctor"), "no archive yet")
	h.ok("add", "Create the archive")
	mustContain(t, h.ok("doctor"), "integrity", "full-text search", "search index")
	var checks []map[string]string
	if err := json.Unmarshal([]byte(h.ok("doctor", "--json")), &checks); err != nil || len(checks) == 0 {
		t.Fatalf("doctor --json: %v", err)
	}
	mustContain(t, h.ok("doctor", "--rebuild-index"), "rebuilt")
	mustContain(t, h.ok("completion", "bash"), "holocron")
	mustContain(t, h.ok("completion", "powershell"), "holocron")
	if r := h.run("frobnicate"); r.code != ExitUsage || !strings.Contains(r.err, "--help") {
		t.Errorf("unknown command: %+v", r)
	}
	if r := h.run("list", "--bogus"); r.code != ExitUsage {
		t.Errorf("unknown flag: exit %d", r.code)
	}
	// With no terminal, bare `holocron` prints help instead of starting the TUI.
	mustContain(t, h.ok(), "Usage:")

	// A broken config file is reported clearly.
	_ = os.WriteFile(filepath.Join(h.dir, "config.toml"), []byte("colour = 1\n"), 0o600)
	if r := h.run("today"); r.code == 0 || !strings.Contains(r.err, "unknown setting") {
		t.Errorf("bad config: %+v", r)
	}
}

func TestGitImport(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	h := newHarness(t)
	repo := filepath.Join(h.dir, "repo")
	gitRun := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+h.dir, "XDG_CONFIG_HOME="+h.dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun("init", "-q", "-b", "main")
	gitRun("config", "user.email", "me@example.com")
	gitRun("config", "user.name", "Me")
	gitRun("config", "commit.gpgsign", "false")
	gitRun("remote", "add", "origin", "git@github.com:example/repo.git")
	for i, msg := range []string{"Add exporter config", "Fix restart loop"} {
		_ = os.WriteFile(filepath.Join(repo, "f.txt"), []byte(msg), 0o644)
		gitRun("add", ".")
		gitRun("commit", "-q", "-m", msg)
		_ = i
	}
	gitRun("config", "user.email", "someone-else@example.com")
	_ = os.WriteFile(filepath.Join(repo, "g.txt"), []byte("x"), 0o644)
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "Someone else's commit")
	gitRun("config", "user.email", "me@example.com")

	h.ok("project", "add", "exporter", "--path", repo)

	dry := h.run("import", "git", "--repo", repo, "--since", "7d")
	if dry.code != 0 {
		t.Fatalf("dry run: %+v", dry)
	}
	mustContain(t, dry.out, "Add exporter config", "Fix restart loop")
	mustNotContain(t, dry.out, "Someone else")
	mustContain(t, dry.err, "nothing imported")
	mustNotContain(t, h.ok("list"), "Fix restart loop")

	mustContain(t, h.ok("import", "git", "--repo", repo, "--since", "7d", "--yes"), "Imported 2 commits")
	again := h.run("import", "git", "--repo", repo, "--since", "7d", "--yes")
	mustContain(t, again.err, "No new commits", "2 already imported")
	list := h.ok("list", "--project", "exporter")
	mustContain(t, list, "Fix restart loop", "Add exporter config")
	show := h.ok("show", "1")
	mustContain(t, show, "Source", "git", "https://github.com/example/repo/commit/")
}

func TestShowPrintsTextOnce(t *testing.T) {
	h := newHarness(t)
	h.ok("add", "A single line entry")
	h.ok("add", "First line of a longer entry\nsecond line")
	if n := strings.Count(h.ok("show", "1"), "A single line entry"); n != 1 {
		t.Errorf("single-line entry printed %d times", n)
	}
	if n := strings.Count(h.ok("show", "2"), "First line of a longer entry"); n != 1 {
		t.Errorf("multi-line entry's first line printed %d times", n)
	}
}

func TestShowMarkdown(t *testing.T) {
	h := newHarness(t)
	h.ok("add", "--raw", "Use _markdown_ and **bold**")
	// Piped: exactly as stored.
	mustContain(t, h.ok("show", "1"), "Use _markdown_ and **bold**")
	// At a terminal: rendered.
	var out, errb bytes.Buffer
	code := Execute(context.Background(), []string{"show", "1"}, IO{In: strings.NewReader(""), Out: &out, Err: &errb, OutTTY: true, Width: 80})
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	plain := stripANSI(out.String())
	if !strings.Contains(plain, "Use markdown and bold") || strings.Contains(plain, "**") {
		t.Fatalf("terminal show not rendered:\n%s", plain)
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	esc := false
	for _, r := range s {
		switch {
		case esc:
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				esc = false
			}
		case r == '\x1b':
			esc = true
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func TestListFitsTerminalWidth(t *testing.T) {
	h := newHarness(t)
	h.ok("add", "Investigated an S3 policy issue where the bucket policy allowed public reads on the logs prefix", "-p", "AWS", "--type", "investigation", "--tag", "security", "--mark", "important")
	h.ok("add", "Short one", "--tag", "security")
	for _, width := range []int{60, 80, 100, 140} {
		for _, args := range [][]string{{"today"}, {"search", "security"}, {"search", "policy"}} {
			var out bytes.Buffer
			code := Execute(context.Background(), args, IO{In: strings.NewReader(""), Out: &out, Err: &bytes.Buffer{}, OutTTY: true, Width: width})
			if code != 0 {
				t.Fatalf("%v exit %d", args, code)
			}
			for _, line := range strings.Split(stripANSI(out.String()), "\n") {
				if w := len([]rune(line)); w >= width {
					t.Errorf("width %d, %v: line is %d cells: %q", width, args, w, line)
				}
			}
		}
	}
}

func TestTypeAliases(t *testing.T) {
	h := newHarness(t)
	h.ok("add", "Win: rotated the keys")
	h.ok("add", "Why does the cache miss", "--type", "look")
	h.ok("add", "Unrelated note", "--type", "note")

	var entries []struct {
		Body string  `json:"body"`
		Type *string `json:"type"`
	}
	if err := json.Unmarshal([]byte(h.ok("list", "--type", "win,look", "--json", "--reverse")), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Body != "rotated the keys" || *entries[0].Type != "accomplishment" || *entries[1].Type != "investigation" {
		t.Fatalf("aliased entries = %+v", entries)
	}
	out := h.ok("list")
	mustContain(t, out, "accomplishment", "investigation")
	mustNotContain(t, out, "win", "look")
}

func TestResolvesFlag(t *testing.T) {
	h := newHarness(t)
	h.ok("add", "Follow-up: ask about ARM capacity")
	h.ok("add", "Problem: exporter restarts")
	h.ok("add", "Note: lunch menu")
	r := h.run("add", "ARM capacity approved", "--resolves", "1", "--resolves", "#3")
	if r.code != 0 {
		t.Fatalf("add --resolves: %+v", r)
	}
	mustContain(t, r.out, "Added", "#4", "Resolved", "#1", "ask about ARM capacity", "#3")
	mustContain(t, r.err, "#3 is a note, not a problem or follow-up")

	mustContain(t, h.ok("show", "1"), "resolved", "by #4")
	mustContain(t, h.ok("show", "4"), "Resolves", "#1, #3")
	mustContain(t, h.ok("list", "--open"), "exporter restarts")
	mustNotContain(t, h.ok("list", "--open"), "ARM capacity")

	var e struct {
		ResolvedBy *int64  `json:"resolved_by"`
		Resolves   []int64 `json:"resolves"`
	}
	if err := json.Unmarshal([]byte(h.ok("show", "1", "--json")), &e); err != nil || e.ResolvedBy == nil || *e.ResolvedBy != 4 {
		t.Fatalf("show 1 --json: %+v %v", e, err)
	}
	if err := json.Unmarshal([]byte(h.ok("show", "4", "--json")), &e); err != nil || e.ResolvedBy != nil || len(e.Resolves) != 2 {
		t.Fatalf("show 4 --json: %+v %v", e, err)
	}
	mustContain(t, h.ok("export", "--format", "markdown"), "follow-up (resolved by #4)", "resolves: #1, #3")
	mustContain(t, h.ok("report", "staff"), "resolves #1: ask about ARM capacity")

	if r := h.run("add", "again", "--resolves", "1"); r.code == 0 || !strings.Contains(r.err, "already resolved") {
		t.Fatalf("resolving twice: %+v", r)
	}
	if n := strings.Count(h.ok("list"), "again"); n != 0 {
		t.Fatal("a failed --resolves saved the entry")
	}
	h.ok("resolve", "1", "--reopen")
	mustNotContain(t, h.ok("show", "1"), "by #4")
	mustContain(t, h.ok("show", "4"), "#3")
	mustNotContain(t, h.ok("show", "4"), "#1, #3")
}

func TestShowTypeAliases(t *testing.T) {
	h := newHarness(t)
	mustContain(t, h.ok("doctor"), "type aliases", "look → investigation, win → accomplishment")
	show := h.ok("config", "show")
	mustContain(t, show, "[type_aliases]", `look = "investigation"`, `win = "accomplishment"`)

	cfg := "[type_aliases]\nship = \"accomplishment\"\nlook = \"\"\n"
	if err := os.WriteFile(filepath.Join(h.dir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	doctor := h.ok("doctor")
	mustContain(t, doctor, "ship → accomplishment, win → accomplishment")
	mustNotContain(t, doctor, "look →")
	show = h.ok("config", "show")
	mustContain(t, show, `ship = "accomplishment"`, `win = "accomplishment"`)
	mustNotContain(t, show, "look =")

	cfg = "[type_aliases]\nwin = \"\"\nlook = \"\"\n"
	if err := os.WriteFile(filepath.Join(h.dir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	mustContain(t, h.ok("doctor"), "none configured")

	// A broken config is reported as such, not as the default aliases.
	cfg = "[type_aliases]\nwin = \"victory\"\n"
	if err := os.WriteFile(filepath.Join(h.dir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	r := h.run("doctor")
	mustContain(t, r.out+r.err, "victory")
	mustNotContain(t, r.out, "look → investigation")
}

func TestReportSinceLast(t *testing.T) {
	h := newHarness(t)
	h.ok("add", "Shipped the January release", "--type", "accomplishment", "--at", "2026-01-15 10:00")
	h.ok("add", "Shipped the February release", "--type", "accomplishment", "--at", "2026-02-03 10:00")

	r := h.run("report", "staff", "--since", "last")
	if r.code != ExitUsage || !strings.Contains(r.err, "no staff report has been recorded yet") {
		t.Fatalf("--since last with nothing recorded: %+v", r)
	}

	r = h.run("report", "staff", "--from", "2026-01-01", "--to", "2026-01-31", "--record")
	if r.code != 0 {
		t.Fatalf("--record: %+v", r)
	}
	mustContain(t, r.out, "January release")
	mustContain(t, r.err, "Recorded this staff report", "Sun 1 Feb")

	out := h.ok("report", "staff", "--since", "last")
	mustContain(t, out, "February release", "since the last staff report")
	mustNotContain(t, out, "January release", "early in the week")
	mustNotContain(t, h.ok("report", "staff", "--since", "last", "--to", "2026-02-02"), "February release")

	if r := h.run("report", "staff", "--since", "last", "--range", "this-week"); r.code != ExitUsage {
		t.Fatalf("--since last --range: %+v", r)
	}
	// Each kind keeps its own record.
	if r := h.run("report", "one-on-one", "--since", "last"); r.code != ExitUsage {
		t.Fatalf("one-on-one --since last: %+v", r)
	}
	// Without --record nothing is remembered.
	h.ok("report", "staff", "--range", "2026-Q1")
	mustContain(t, h.ok("report", "staff", "--since", "last"), "February release")
}

func TestImportJSON(t *testing.T) {
	h := newHarness(t)
	h.ok("add", "Follow-up: ask about ARM capacity +infra #arm")
	h.ok("add", "Shipped the exporter +infra", "--type", "accomplishment", "--mark", "staff")
	h.ok("add", "ARM approved", "--resolves", "1")
	h.ok("project", "edit", "infra", "--alias", "inf")
	file := filepath.Join(h.dir, "export.json")
	h.ok("export", "--format", "json", "-o", file)

	other := filepath.Join(h.dir, "other.db")
	in := func(args ...string) string { return h.ok(append([]string{"--db", other}, args...)...) }
	in("add", "Something already here")

	dry := in("import", "json", file, "--dry-run")
	mustContain(t, dry, "Would add 3 entries and 1 project", "Would renumber", "#1 → #4")
	mustNotContain(t, in("list"), "ARM approved")

	r := h.run("--db", other, "import", "json", file)
	if r.code != 0 {
		t.Fatalf("import: %+v", r)
	}
	mustContain(t, r.out, "Added 3 entries and 1 project", "#1 → #4")
	mustContain(t, r.err, "Backup of the archive before importing")
	mustContain(t, in("show", "4"), "ask about ARM capacity", "infra", "resolved", "by #3")
	mustContain(t, in("show", "3"), "Resolves", "#4")
	mustContain(t, in("search", "+inf"), "Shipped the exporter")
	mustContain(t, in("show", "1"), "Something already here")

	// Importing again changes nothing; a newer edit here survives an older file.
	in("edit", "2", "--text", "Shipped the exporter (edited later)")
	mustContain(t, in("import", "json", file), "Nothing to import")
	mustContain(t, in("show", "2"), "edited later")
	in("delete", "3", "--yes")
	mustContain(t, in("import", "json", file), "Nothing to import", "Skipped 1 entry deleted here")

	var summary struct {
		DryRun  bool           `json:"dry_run"`
		Entries map[string]int `json:"entries"`
	}
	if err := json.Unmarshal([]byte(in("import", "json", file, "--dry-run", "--json")), &summary); err != nil || !summary.DryRun || summary.Entries["skipped"] != 1 {
		t.Fatalf("--json summary = %+v, %v", summary, err)
	}
	// The original archive imports its own export as a no-op.
	mustContain(t, h.ok("import", "json", file), "Nothing to import")

	bad := filepath.Join(h.dir, "bad.json")
	_ = os.WriteFile(bad, []byte(`{"format": "something/v9"}`), 0o600)
	if r := h.run("import", "json", bad); r.code == 0 || !strings.Contains(r.err, "unsupported export format") {
		t.Fatalf("bad format: %+v", r)
	}
}
