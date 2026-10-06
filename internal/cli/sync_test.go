package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Unscheduled-Maintenance/Holocron/internal/devsync"
)

func init() {
	// Never touch the real OS keychain from tests, and keep passphrases fast.
	keychain = devsync.MemoryKeychain{}
	devsync.ScryptWorkFactor = 10
}

func TestSyncCommands(t *testing.T) {
	h := newHarness(t)
	folder := filepath.Join(h.dir, "OneDrive", "Holocron")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	mustContain(t, h.ok("sync", "status"), "not set up")
	if r := h.run("sync"); r.code != ExitUsage {
		t.Fatalf("sync before init: %+v", r)
	}

	h.ok("add", "Written before sync")
	out := h.ok("sync", "init", folder, "--name", "work")
	mustContain(t, out, "Sync is set up", `device "a"`, "#1a", "sync join")
	key := regexp.MustCompile(`AGE-SECRET-KEY-PQ-1[A-Z0-9]+`).FindString(out)
	if key == "" {
		t.Fatalf("no key in:\n%s", out)
	}
	mustContain(t, h.ok("add", "Written after sync"), "#2a")
	if got := strings.TrimSpace(h.ok("sync", "key", "show")); got != key {
		t.Fatalf("key show = %q", got)
	}

	other := filepath.Join(h.dir, "home.db")
	home := func(args ...string) string { return h.ok(append([]string{"--db", other}, args...)...) }
	home("add", "Already on the home laptop")
	r := h.runIn(key+"\n", "--db", other, "sync", "join", folder, "--key", "--name", "home")
	if r.code != 0 {
		t.Fatalf("join: %+v", r)
	}
	mustContain(t, r.out, "Joined", `device "b"`, "2 new entries", "#1 → #1b")
	mustContain(t, home("show", "1"), "Written before sync")
	mustContain(t, home("show", "2a"), "Written after sync")
	mustContain(t, home("show", "1b"), "Already on the home laptop")

	// Ordinary commands sync by themselves.
	mustContain(t, home("add", "From home"), "#2b")
	mustContain(t, h.ok("search", "home"), "From home", "Already on the home laptop")
	h.ok("edit", "2b", "--text", "From home, tidied at work")
	mustContain(t, home("show", "2b"), "tidied at work")

	status := h.ok("sync", "status")
	mustContain(t, status, folder, "device a", "work", "home", "this computer")
	var st struct {
		Enabled bool `json:"enabled"`
		Label   string
		Devices []struct {
			Label string `json:"label"`
		} `json:"devices"`
	}
	if err := json.Unmarshal([]byte(h.ok("sync", "status", "--json")), &st); err != nil || !st.Enabled || len(st.Devices) != 2 {
		t.Fatalf("status --json = %+v, %v", st, err)
	}
	mustContain(t, h.ok("doctor"), "sync", "device a with 2 computers")
	mustContain(t, h.ok("sync"), "Synced.")
	mustContain(t, h.ok("sync", "compact"), "Compacted.")

	// A wrong key does not join.
	third := filepath.Join(h.dir, "third.db")
	if r := h.runIn("AGE-SECRET-KEY-PQ-1WRONG\n", "--db", third, "sync", "join", folder, "--key"); r.code == 0 {
		t.Fatalf("joined with a wrong key: %+v", r)
	}

	if r := h.run("sync", "off"); r.code != ExitUsage {
		t.Fatalf("off without --yes: %+v", r)
	}
	mustContain(t, h.ok("sync", "off", "--yes"), "Sync is off")
	mustContain(t, h.ok("sync", "status"), "not set up")
	mustContain(t, h.ok("add", "After sync was turned off"), "#3a")
}

func TestSyncReceiveOnly(t *testing.T) {
	h := newHarness(t)
	folder := filepath.Join(h.dir, "OneDrive", "Holocron")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	if r := h.run("sync", "mode"); r.code != ExitUsage {
		t.Fatalf("mode before init: %+v", r)
	}
	out := h.ok("sync", "init", folder, "--name", "home")
	key := regexp.MustCompile(`AGE-SECRET-KEY-PQ-1[A-Z0-9]+`).FindString(out)
	mustContain(t, h.ok("sync", "mode"), "two-way")
	h.ok("add", "Personal note")

	work := filepath.Join(h.dir, "work.db")
	atWork := func(args ...string) string { return h.ok(append([]string{"--db", work}, args...)...) }
	r := h.runIn(key+"\n", "--db", work, "sync", "join", folder, "--key", "--name", "work", "--receive-only")
	if r.code != 0 {
		t.Fatalf("join: %+v", r)
	}
	mustContain(t, r.out, "Joined", "receive-only")
	mustContain(t, atWork("show", "1a"), "Personal note")
	atWork("add", "Work note")
	mustContain(t, atWork("sync", "mode"), "receive-only")
	mustContain(t, atWork("sync", "status"), "receive-only")
	if strings.Contains(atWork("sync", "status"), "waiting") {
		t.Fatal("status shows waiting changes on a receive-only computer")
	}
	mustContain(t, atWork("doctor"), "receive-only")
	if strings.Contains(h.ok("search", "note"), "Work note") {
		t.Fatal("a receive-only computer's note reached the other computer")
	}

	if r := h.run("--db", work, "sync", "mode", "two-way"); r.code != ExitUsage {
		t.Fatalf("two-way without --yes: %+v", r)
	}
	if r := h.run("--db", work, "sync", "mode", "sideways"); r.code != ExitUsage {
		t.Fatalf("unknown mode: %+v", r)
	}
	mustContain(t, atWork("sync", "mode", "two-way", "--yes"), "two ways")
	mustContain(t, h.ok("search", "note"), "Work note")
	mustContain(t, atWork("sync", "mode", "receive-only"), "receive-only")
	mustContain(t, atWork("sync", "mode", "receive-only"), "already receive-only")
}
