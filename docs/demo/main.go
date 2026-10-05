// Command demo regenerates the README's GIFs and screenshots with VHS.
//
//	go run ./docs/demo              # every tape in docs/demo/tapes
//	go run ./docs/demo tui search   # only the named tapes
//
// It builds Holocron, seeds a throwaway archive with realistic entries dated
// relative to today, and runs each tape with the shared Holocron theme
// (docs/demo/style.tape). Nothing touches your real archive.
//
// Requirements: VHS v0.11.x, ttyd and ffmpeg on PATH. VHS v0.12.0–v0.12.1
// cancel their own context before rendering and silently write no images,
// so they are rejected. See docs/demo/README.md.
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/Unscheduled-Maintenance/Holocron/internal/app"
	"github.com/Unscheduled-Maintenance/Holocron/internal/journal"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "demo:", err)
		os.Exit(1)
	}
}

func run(only []string) error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	if err := checkTools(); err != nil {
		return err
	}

	work, err := os.MkdirTemp("", "holocron-demo-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	bin := filepath.Join(work, "bin")
	exe := filepath.Join(bin, "holocron")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	fmt.Println("building holocron…")
	build := exec.Command("go", "build", "-o", exe, "./cmd/holocron")
	build.Dir, build.Stdout, build.Stderr = root, os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		return fmt.Errorf("building holocron: %w", err)
	}

	cfg := filepath.Join(work, "config.toml")
	if err := os.WriteFile(cfg, []byte(demoConfig), 0o600); err != nil {
		return err
	}

	tapes, err := filepath.Glob(filepath.Join(root, "docs", "demo", "tapes", "*.tape"))
	if err != nil {
		return err
	}
	style, err := os.ReadFile(filepath.Join(root, "docs", "demo", "style.tape"))
	if err != nil {
		return err
	}
	ran := 0
	for _, tape := range tapes {
		name := strings.TrimSuffix(filepath.Base(tape), ".tape")
		if len(only) > 0 && !slices.Contains(only, name) {
			continue
		}
		fmt.Printf("recording %s…\n", name)
		// Every tape starts from the same freshly seeded archive, so one
		// tape's changes never appear in another's images.
		data := filepath.Join(work, name, "data")
		if err := seed(data, cfg); err != nil {
			return fmt.Errorf("seeding: %w", err)
		}
		env := append(os.Environ(),
			"HOLOCRON_DATA_DIR="+data,
			"HOLOCRON_CONFIG="+cfg,
			"HOLOCRON_DB=",
			"NO_COLOR=",
			"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		)
		full, err := assemble(tape, string(style), work)
		if err != nil {
			return err
		}
		tmp := filepath.Join(work, name+".tape")
		if err := os.WriteFile(tmp, []byte(full), 0o600); err != nil {
			return err
		}
		vhs := exec.Command("vhs", tmp)
		vhs.Dir, vhs.Env = root, env
		vhs.Stdout, vhs.Stderr = os.Stdout, os.Stderr
		if err := vhs.Run(); err != nil {
			return fmt.Errorf("vhs %s: %w", name, err)
		}
		ran++
	}
	if ran == 0 {
		return fmt.Errorf("no tapes matched %v", only)
	}
	fmt.Printf("recorded %d tape(s) into docs/images\n", ran)
	return nil
}

func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("run this from inside the Holocron repository")
		}
		dir = parent
	}
}

func checkTools() error {
	for _, t := range []string{"vhs", "ttyd", "ffmpeg"} {
		if _, err := exec.LookPath(t); err != nil {
			return fmt.Errorf("%s is not on PATH (see docs/demo/README.md)", t)
		}
	}
	out, err := exec.Command("vhs", "--version").Output()
	if err != nil {
		return fmt.Errorf("running vhs --version: %w", err)
	}
	v := string(out)
	if strings.Contains(v, "v0.12.0") || strings.Contains(v, "v0.12.1") {
		return fmt.Errorf("%s has a bug that silently skips rendering; install v0.11.0: go install github.com/charmbracelet/vhs@v0.11.0", strings.TrimSpace(v))
	}
	return nil
}

// assemble builds a runnable tape: the tape's Output lines, the shell, the
// shared style, the tape's own Set lines (overrides), a hidden prompt set-up,
// then the body.
// VHS ignores settings that come after the first command, which is why the
// pieces are joined in this order.
func assemble(path, style, scratch string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var outputs, settings, body []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch t := strings.TrimSpace(line); {
		case strings.HasPrefix(t, "Output "):
			outputs = append(outputs, line)
		case strings.HasPrefix(t, "Set "):
			settings = append(settings, line) // tape-specific overrides of the style
		default:
			body = append(body, line)
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	if len(outputs) == 0 {
		// Screenshot-only tape: VHS still renders a GIF, so send it to scratch.
		outputs = append(outputs, fmt.Sprintf("Output %q", filepath.ToSlash(filepath.Join(scratch, "discard.gif"))))
	}
	shell, setup := shellSetup()
	var b strings.Builder
	b.WriteString(strings.Join(outputs, "\n") + "\n")
	fmt.Fprintf(&b, "Set Shell %q\n", shell)
	b.WriteString(style + "\n")
	b.WriteString(strings.Join(settings, "\n") + "\n")
	b.WriteString("Hide\n")
	for _, s := range setup {
		b.WriteString("Type `" + s + "`\nEnter\n")
	}
	b.WriteString("Type `clear`\nEnter\nSleep 500ms\nShow\n")
	b.WriteString(strings.Join(body, "\n") + "\n")
	return b.String(), nil
}

// shellSetup chooses the recording shell and the commands that give it a
// Holocron-coloured prompt. In PowerShell it also turns off history-based
// suggestions so nothing from the person's own history appears on screen.
func shellSetup() (string, []string) {
	if runtime.GOOS == "windows" {
		shell := "pwsh"
		if _, err := exec.LookPath("pwsh"); err != nil {
			shell = "powershell"
		}
		return shell, []string{
			`Set-PSReadLineOption -PredictionSource None -HistorySaveStyle SaveNothing; [Microsoft.PowerShell.PSConsoleReadLine]::ClearHistory()`,
			`function prompt { "$([char]27)[38;5;44m›$([char]27)[0m " }`,
		}
	}
	return "bash", []string{`PS1='\[\e[38;5;44m\]›\[\e[0m\] '`}
}

const demoConfig = `# Configuration used only for the README recordings.
[tui]
default_range = "7d"
[reports]
staff_range = "7d"
`

// seed fills the archive with a week of realistic entries. Dates are
// relative to today so the recordings always show recent activity.
func seed(dataDir, cfg string) error {
	ctx := context.Background()
	if err := os.Setenv("HOLOCRON_DATA_DIR", dataDir); err != nil {
		return err
	}
	a, err := app.Open(ctx, app.Options{ConfigPath: cfg})
	if err != nil {
		return err
	}
	defer a.Close()

	if _, err := a.Store.CreateProject(ctx, journal.NewProject{Name: "AWS", Aliases: []string{"aws"}, Description: "Cloud accounts and guardrails"}); err != nil {
		return err
	}
	if _, err := a.Store.CreateProject(ctx, journal.NewProject{Name: "Infra", Aliases: []string{"infra", "ops"}, Description: "Servers, CI and monitoring"}); err != nil {
		return err
	}

	now := time.Now()
	day := func(back int, hhmm string) string {
		return now.AddDate(0, 0, -back).Format("2006-01-02") + " " + hhmm
	}
	ago := func(d time.Duration) string { return now.Add(-d).Format("2006-01-02 15:04") }

	type e struct {
		at, text, typ, project string
		tags, marks            []string
	}
	entries := []e{
		{at: day(6, "09:20"), text: "Enabled IAM Access Analyzer in all AWS regions +aws #security", typ: "accomplishment", marks: []string{"staff"}},
		{at: day(6, "14:05"), text: "Reviewed the repository moderation queue +CCR", typ: "work"},
		{at: day(5, "10:40"), text: "Investigated unexpected exporter restarts +infra #monitoring", typ: "investigation"},
		{at: day(5, "16:15"), text: "Exporter restarted again after the kernel update +infra #monitoring", typ: "problem"},
		{at: day(4, "09:05"), text: "Decision: retain the existing deployment model for now +infra #architecture", typ: "decision", marks: []string{"staff"}},
		{at: day(4, "11:30"), text: "Fixed CloudTrail bucket logging policy +aws #security #logging", typ: "accomplishment"},
		{at: day(3, "13:45"), text: "Paired with Sam on Terraform module layout +infra #terraform", typ: "work"},
		{at: day(3, "17:10"), text: "Follow up with engineering about ARM build capacity +infra", typ: "follow-up", marks: []string{"cross-team"}},
		{at: day(2, "10:00"), text: "Pinned exporter to the previous kernel; restarts stopped +infra #monitoring", typ: "accomplishment"},
		{at: day(2, "15:30"), text: "Want to talk about on-call load in my next one-on-one", marks: []string{"one-on-one"}},
		{at: day(1, "09:50"), text: "Rotated deploy keys for every AWS account +aws #security", typ: "work"},
		{at: day(1, "14:20"), project: "aws", tags: []string{"security"}, text: "Investigated an S3 policy issue\n\n- Bucket policy allowed **public reads** on `logs/`\n- Fixed by scoping to the _org ID_\n- Added a guardrail check", typ: "investigation", marks: []string{"important"}},
		{at: ago(3 * time.Hour), text: "Reviewed the backup restore runbook +infra #backups", typ: "work"},
		{at: ago(90 * time.Minute), text: "Lunch-and-learn on SQLite WAL mode", typ: "note"},
	}
	var restartProblem int64
	for _, x := range entries {
		res, err := a.Capture(ctx, app.CaptureInput{Text: x.text, Type: x.typ, Project: x.project, Tags: x.tags, Marks: x.marks, At: x.at})
		if err != nil {
			return fmt.Errorf("%q: %w", x.text, err)
		}
		if strings.HasPrefix(x.text, "Exporter restarted again") {
			restartProblem = res.Entry.ID
		}
	}
	// The exporter problem was fixed two days later.
	yes := true
	_, err = a.Store.Update(ctx, restartProblem, journal.Patch{Resolved: &yes})
	return err
}
