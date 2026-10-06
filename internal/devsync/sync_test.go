package devsync

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/Unscheduled-Maintenance/Holocron/internal/database"
	"github.com/Unscheduled-Maintenance/Holocron/internal/journal"
)

func TestMain(m *testing.M) {
	// Act as a key_command when asked to (see keyCommandFor).
	if key := os.Getenv("HOLOCRON_TEST_PRINT_KEY"); key != "" {
		fmt.Println(key)
		os.Exit(0)
	}
	ScryptWorkFactor = 10
	os.Exit(m.Run())
}

func keyCommandFor(t *testing.T, key string) []string {
	t.Helper()
	t.Setenv("HOLOCRON_TEST_PRINT_KEY", key)
	return []string{os.Args[0]}
}

type computer struct {
	t     *testing.T
	store *journal.Store
	sync  *Syncer
	now   time.Time
}

var t0 = time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)

func newComputer(t *testing.T, start time.Time) *computer {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "h.db"), database.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	c := &computer{t: t, now: start}
	clock := func() time.Time { return c.now }
	c.store = journal.NewStore(db, journal.WithClock(clock), journal.WithLocation(time.UTC))
	c.sync = &Syncer{Store: c.store, Keychain: MemoryKeychain{}, Now: clock}
	return c
}

func (c *computer) later(d time.Duration) *computer { c.now = c.now.Add(d); return c }

func (c *computer) add(body string) journal.Entry {
	c.t.Helper()
	e, err := c.store.AddEntry(context.Background(), journal.NewEntry{Body: body})
	if err != nil {
		c.t.Fatal(err)
	}
	return e
}

func (c *computer) get(ref string) journal.Entry {
	c.t.Helper()
	e, err := c.store.Get(context.Background(), ref)
	if err != nil {
		c.t.Fatalf("Get(%s): %v", ref, err)
	}
	return e
}

func (c *computer) push() PushResult {
	c.t.Helper()
	r, err := c.sync.Push(context.Background())
	if err != nil {
		c.t.Fatalf("push: %v", err)
	}
	return r
}

func (c *computer) pull() PullResult {
	c.t.Helper()
	r, err := c.sync.Pull(context.Background())
	if err != nil {
		c.t.Fatalf("pull: %v", err)
	}
	return r
}

func sshKeyPair(t *testing.T) (authorized, privatePath string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	privatePath = filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(privatePath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return string(ssh.MarshalAuthorizedKey(sp)), privatePath
}

func TestInitJoinAndSync(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	authorized, private := sshKeyPair(t)

	a := newComputer(t, t0)
	a.add("a one")
	a.add("a two")
	res, err := a.sync.Init(ctx, dir, InitOptions{Name: "work laptop", Passphrase: "correct horse battery", SSHPublicKeys: []string{authorized}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Label != "a" || !strings.HasPrefix(res.Key, "AGE-SECRET-KEY-PQ-1") {
		t.Fatalf("init = %+v", res)
	}
	for _, p := range []string{"format.json", "keys/passphrase.age"} {
		if _, err := os.Stat(filepath.Join(dir, DirName, p)); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
	if ssh, _ := filepath.Glob(filepath.Join(dir, DirName, "keys", "ssh-*.age")); len(ssh) != 1 {
		t.Errorf("ssh unlock files = %v", ssh)
	}
	if _, err := a.sync.Init(ctx, dir, InitOptions{}); err == nil {
		t.Fatal("initialising twice must fail")
	}
	// Existing entries keep plain numbers; new ones are labelled after them.
	if got := a.add("a three").Ref(); got != "#3a" {
		t.Fatalf("first labelled entry = %s", got)
	}
	a.push()

	// b already has an entry of its own, which takes b's label on joining.
	b := newComputer(t, t0.Add(time.Hour))
	mine := b.add("b's own")
	if _, err := b.sync.Join(ctx, dir, Unlock{Passphrase: "wrong passphrase"}, "home"); err == nil {
		t.Fatal("a wrong passphrase must not unlock")
	}
	join, err := b.sync.Join(ctx, dir, Unlock{Passphrase: "correct horse battery"}, "home")
	if err != nil {
		t.Fatal(err)
	}
	if join.Label != "b" || len(join.Renumbered) != 1 || join.Renumbered[0].To != "#1b" || join.Pull.Applied.EntriesAdded != 3 {
		t.Fatalf("join = %+v", join)
	}
	if b.get(mine.UID).Ref() != "#1b" || b.get("1").Body != "a one" || b.get("3a").Body != "a three" {
		t.Fatal("numbers after joining are wrong")
	}
	if got := b.add("b new").Ref(); got != "#3b" {
		t.Fatalf("b's next number = %s (must skip the plain range)", got)
	}
	b.push()

	// a picks up b's entries; edits and deletes flow both ways.
	if r := a.pull(); r.Applied.EntriesAdded != 2 {
		t.Fatalf("a pulled %+v", r.Applied)
	}
	if a.get("1b").Body != "b's own" {
		t.Fatal("a does not have #1b")
	}
	body := "a one, edited on b"
	b.later(time.Minute)
	if _, err := b.store.Update(ctx, b.get("1").ID, journal.Patch{Body: &body}); err != nil {
		t.Fatal(err)
	}
	if err := b.store.Delete(ctx, b.get("2").ID); err != nil {
		t.Fatal(err)
	}
	b.push()
	a.later(2 * time.Hour).pull()
	if a.get("1").Body != body {
		t.Error("edit did not arrive")
	}
	if _, err := a.store.Get(ctx, "2"); !errors.Is(err, journal.ErrNotFound) {
		t.Error("delete did not arrive")
	}
	if r := a.pull(); r.Files != 0 {
		t.Errorf("a second pull read %d files", r.Files)
	}

	// Other unlock methods work on further computers.
	c := newComputer(t, t0.Add(2*time.Hour))
	if _, err := c.sync.Join(ctx, dir, Unlock{SSHKey: private}, "spare"); err != nil {
		t.Fatalf("join with SSH key: %v", err)
	}
	d := newComputer(t, t0.Add(3*time.Hour))
	d.sync.KeyCommand = keyCommandFor(t, res.Key)
	if j, err := d.sync.Join(ctx, dir, Unlock{KeyCommand: true}, "tablet"); err != nil || j.Label != "d" {
		t.Fatalf("join with key_command: %+v, %v", j, err)
	}
	e := newComputer(t, t0.Add(4*time.Hour))
	if _, err := e.sync.Join(ctx, dir, Unlock{Key: res.Key}, ""); err != nil {
		t.Fatalf("join with the key: %v", err)
	}
	if e.get("3b").Body != "b new" {
		t.Fatal("a late joiner must see everything")
	}

	st, err := a.sync.Status(ctx)
	if err != nil || st.Label != "a" || len(st.Devices) != 5 || !st.Devices[0].Self || st.Devices[1].Name != "home" || st.Locked {
		t.Fatalf("status = %+v, %v", st, err)
	}
}

func TestLockedAndKeyCommand(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	a := newComputer(t, t0)
	res, err := a.sync.Init(ctx, dir, InitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	b := newComputer(t, t0)
	if _, err := b.sync.Join(ctx, dir, Unlock{Key: res.Key}, ""); err != nil {
		t.Fatal(err)
	}
	// A new process on b with an empty keychain is locked once there is
	// something to read.
	b.sync = &Syncer{Store: b.store, Keychain: MemoryKeychain{}, Now: b.sync.Now}
	a.add("new on a")
	a.push()
	if _, err := b.sync.Pull(ctx); !errors.Is(err, ErrLocked) {
		t.Fatalf("pull while locked: %v", err)
	}
	if st, _ := b.sync.Status(ctx); !st.Locked {
		t.Fatal("status should say locked")
	}
	// key_command unlocks and caches the key.
	b.sync.KeyCommand = keyCommandFor(t, res.Key)
	if r := b.pull(); r.Applied.EntriesAdded != 1 {
		t.Fatalf("pull with key_command: %+v", r)
	}
	if keys, _ := b.sync.Keychain.Get(mustConfig(t, b).Set); len(keys) != 1 {
		t.Fatalf("key not cached: %v", keys)
	}
	if err := b.sync.Unlock(ctx, Unlock{Key: "AGE-SECRET-KEY-PQ-1NOTAKEY"}); err == nil {
		t.Fatal("a bad key unlocked")
	}
}

func mustConfig(t *testing.T, c *computer) Config {
	t.Helper()
	cfg, err := c.sync.Configured(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestCompaction(t *testing.T) {
	ctx := context.Background()
	defer func(n int) { CompactAfterFiles = n }(CompactAfterFiles)
	CompactAfterFiles = 3
	dir := t.TempDir()
	a := newComputer(t, t0)
	res, _ := a.sync.Init(ctx, dir, InitOptions{})
	b := newComputer(t, t0)
	if _, err := b.sync.Join(ctx, dir, Unlock{Key: res.Key}, ""); err != nil {
		t.Fatal(err)
	}
	selfA, _ := a.store.SelfDevice(ctx)
	f := folderAt(dir)
	compacted := false
	for i := 0; i < 6; i++ {
		a.later(time.Minute).add(fmt.Sprintf("entry %d", i))
		if a.push().Compacted {
			compacted = true
		}
		if i == 1 {
			b.pull() // b reads some change files, then misses the rest
		}
	}
	if !compacted {
		t.Fatal("never compacted")
	}
	files, _ := f.listRecordFiles(selfA.UID)
	snapshots := 0
	for _, rf := range files {
		if rf.snapshot {
			snapshots++
		}
	}
	if snapshots != 1 || len(files) > CompactAfterFiles+1 {
		t.Fatalf("files after compaction: %+v", files)
	}
	b.pull()
	for i := 0; i < 6; i++ {
		if es, _ := b.store.Find(ctx, journal.Query{Text: fmt.Sprintf(`"entry %d"`, i)}); len(es) != 1 {
			t.Errorf("b is missing entry %d after compaction", i)
		}
	}
	// A computer that never pushed changes still has a snapshot, so a new
	// joiner gets everything.
	if err := a.sync.Compact(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRotateKey(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	a := newComputer(t, t0)
	res, _ := a.sync.Init(ctx, dir, InitOptions{})
	b := newComputer(t, t0)
	if _, err := b.sync.Join(ctx, dir, Unlock{Key: res.Key}, ""); err != nil {
		t.Fatal(err)
	}
	b.add("before rotation")
	b.push()
	newKey, err := a.sync.RotateKey(ctx, InitOptions{Passphrase: "a brand new passphrase"})
	if err != nil || newKey == res.Key {
		t.Fatalf("rotate = %v", err)
	}
	a.add("after rotation")
	a.push()
	// a can still read b's files written with the old key.
	if r := a.pull(); r.Applied.EntriesAdded != 1 {
		t.Fatalf("a after rotation: %+v", r)
	}
	if _, err := b.sync.Pull(ctx); !errors.Is(err, ErrLocked) {
		t.Fatalf("b with only the old key: %v", err)
	}
	if err := b.sync.Unlock(ctx, Unlock{Key: res.Key}); err == nil {
		t.Fatal("the old key must no longer unlock")
	}
	if err := b.sync.Unlock(ctx, Unlock{Passphrase: "a brand new passphrase"}); err != nil {
		t.Fatal(err)
	}
	if r := b.pull(); r.Applied.EntriesAdded != 1 {
		t.Fatalf("b after unlocking: %+v", r)
	}
	b.add("b after rotation")
	b.push()
	a.pull()
	if es, _ := a.store.Find(ctx, journal.Query{Text: "rotation"}); len(es) != 3 {
		t.Fatalf("a has %d of 3 entries", len(es))
	}
}

func TestLabelClashAndRelabel(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	a := newComputer(t, t0)
	res, _ := a.sync.Init(ctx, dir, InitOptions{})
	b := newComputer(t, t0)
	if _, err := b.sync.Join(ctx, dir, Unlock{Key: res.Key}, "b"); err != nil {
		t.Fatal(err)
	}
	key, _ := parseDataKey(res.Key)
	f := folderAt(dir)
	clash := func(uid string) {
		if err := writeDevice(filepath.Join(f.deviceDir(uid), "device.age"), journal.DeviceRecord{UID: uid, Label: "b", Name: "impostor"}, key); err != nil {
			t.Fatal(err)
		}
		if err := writeRecords(filepath.Join(f.deviceDir(uid), recordFileName(true, 1)), journal.Records{}, key); err != nil {
			t.Fatal(err)
		}
	}
	// A device registered after b with the same label is skipped by b.
	newer := journal.NewUID(t0.Add(time.Hour))
	clash(newer)
	if r := b.pull(); len(r.Warnings) == 0 || !strings.Contains(r.Warnings[0], "relabel") {
		t.Fatalf("warnings = %v", r.Warnings)
	}
	// One registered before b makes b relabel.
	_ = os.RemoveAll(f.deviceDir(newer))
	clash("00000000000000000000000000")
	if _, err := b.sync.Pull(ctx); err == nil || !strings.Contains(err.Error(), "relabel") {
		t.Fatalf("pull with an older clash: %v", err)
	}
	e := b.add("numbered before relabelling")
	label, err := b.sync.Relabel(ctx)
	if err != nil || label != "c" {
		t.Fatalf("relabel = %q, %v", label, err)
	}
	if got := b.get(e.UID).Ref(); got != "#"+fmt.Sprint(e.Num)+"c" {
		t.Fatalf("after relabelling: %s", got)
	}
	a.pull()
	if got := a.get(e.UID).Ref(); !strings.HasSuffix(got, "c") {
		t.Fatalf("a sees %s", got)
	}
}

func TestOff(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	a := newComputer(t, t0)
	if _, err := a.sync.Init(ctx, dir, InitOptions{}); err != nil {
		t.Fatal(err)
	}
	cfg := mustConfig(t, a)
	if err := a.sync.Off(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := a.sync.Configured(ctx); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("still configured: %v", err)
	}
	if keys, _ := a.sync.Keychain.Get(cfg.Set); len(keys) != 0 {
		t.Fatal("key left in the keychain")
	}
	if got := a.add("after off").Ref(); got != "#1a" {
		t.Fatalf("numbering after off = %s", got)
	}
}
