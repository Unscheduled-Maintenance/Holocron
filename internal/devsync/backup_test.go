package devsync

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePlain(t *testing.T) (path string, content []byte) {
	t.Helper()
	content = []byte(strings.Repeat("SQLite format 3\x00 pretend archive ", 100))
	path = filepath.Join(t.TempDir(), "plain.db")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, content
}

func roundTrip(t *testing.T, s *Syncer, lock BackupLock, u Unlock) (BackupKind, error) {
	t.Helper()
	ctx := context.Background()
	src, want := writePlain(t)
	dir := t.TempDir()
	enc := filepath.Join(dir, "b.db.age")
	if err := s.EncryptBackup(ctx, src, enc, lock); err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if raw, _ := os.ReadFile(enc); strings.Contains(string(raw), "pretend archive") {
		t.Fatal("backup is not encrypted")
	}
	kind, err := InspectBackup(enc)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.db")
	if err := s.DecryptBackup(ctx, enc, out, u); err != nil {
		if _, serr := os.Stat(out); serr == nil {
			t.Error("a failed decryption left a file behind")
		}
		return kind, err
	}
	if got, _ := os.ReadFile(out); string(got) != string(want) {
		t.Fatal("decrypted backup differs from the original")
	}
	return kind, nil
}

func TestBackupWithPassphrase(t *testing.T) {
	s := &Syncer{}
	kind, err := roundTrip(t, s, BackupLock{Passphrase: "correct horse battery"}, Unlock{Passphrase: "correct horse battery"})
	if err != nil {
		t.Fatal(err)
	}
	if !kind.Encrypted || !kind.Passphrase || kind.SSH || kind.Key {
		t.Fatalf("kind = %+v", kind)
	}
	if _, err := roundTrip(t, s, BackupLock{Passphrase: "correct horse battery"}, Unlock{Passphrase: "wrong horse battery"}); err == nil {
		t.Fatal("opened with the wrong passphrase")
	}
	if err := s.EncryptBackup(context.Background(), "x", filepath.Join(t.TempDir(), "y"), BackupLock{Passphrase: "short"}); err == nil {
		t.Fatal("accepted a short passphrase")
	}
}

func TestBackupWithSSHKey(t *testing.T) {
	authorized, private := sshKeyPair(t)
	other, otherPrivate := sshKeyPair(t)
	s := &Syncer{}
	lock := BackupLock{SSHPublicKeys: []string{authorized, other}}
	kind, err := roundTrip(t, s, lock, Unlock{SSHKey: otherPrivate})
	if err != nil {
		t.Fatal(err)
	}
	if !kind.SSH || kind.Passphrase || kind.Key {
		t.Fatalf("kind = %+v", kind)
	}
	if _, err := roundTrip(t, s, BackupLock{SSHPublicKeys: []string{other}}, Unlock{SSHKey: private}); err == nil {
		t.Fatal("opened with a different SSH key")
	}
}

func TestBackupWithSyncKey(t *testing.T) {
	ctx := context.Background()
	c := newComputer(t, t0)
	res, err := c.sync.Init(ctx, t.TempDir(), InitOptions{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	// No unlock given: the key this computer keeps opens it.
	kind, err := roundTrip(t, c.sync, BackupLock{SyncKey: true}, Unlock{})
	if err != nil {
		t.Fatal(err)
	}
	if !kind.Key || kind.SSH || kind.Passphrase {
		t.Fatalf("kind = %+v", kind)
	}

	// A backup made before a key rotation still opens here.
	src, _ := writePlain(t)
	old := filepath.Join(t.TempDir(), "old.db.age")
	if err := c.sync.EncryptBackup(ctx, src, old, BackupLock{SyncKey: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.sync.RotateKey(ctx, InitOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.sync.DecryptBackup(ctx, old, filepath.Join(t.TempDir(), "o.db"), Unlock{}); err != nil {
		t.Fatalf("backup from before the rotation: %v", err)
	}

	// Another computer without sync opens it with the key, or key_command.
	other := &Syncer{}
	if err := other.DecryptBackup(ctx, old, filepath.Join(t.TempDir(), "o.db"), Unlock{}); err == nil || !strings.Contains(err.Error(), "--key") {
		t.Fatalf("without a key: %v", err)
	}
	if err := other.DecryptBackup(ctx, old, filepath.Join(t.TempDir(), "o.db"), Unlock{Key: res.Key}); err != nil {
		t.Fatalf("with the key: %v", err)
	}
	other.KeyCommand = keyCommandFor(t, res.Key)
	if err := other.DecryptBackup(ctx, old, filepath.Join(t.TempDir(), "o.db"), Unlock{}); err != nil {
		t.Fatalf("with key_command: %v", err)
	}
}

func TestBackupLockErrors(t *testing.T) {
	ctx := context.Background()
	src, _ := writePlain(t)
	authorized, _ := sshKeyPair(t)
	for name, lock := range map[string]BackupLock{
		"nothing":               {},
		"passphrase and ssh":    {Passphrase: "correct horse battery", SSHPublicKeys: []string{authorized}},
		"sync key without sync": {SyncKey: true},
		"sync key and ssh":      {SyncKey: true, SSHPublicKeys: []string{authorized}},
		"bad ssh key":           {SSHPublicKeys: []string{"not a key"}},
	} {
		c := newComputer(t, t0)
		dst := filepath.Join(t.TempDir(), "b.age")
		if err := c.sync.EncryptBackup(ctx, src, dst, lock); err == nil {
			t.Errorf("%s: no error", name)
		}
		if _, err := os.Stat(dst); err == nil {
			t.Errorf("%s: left a file behind", name)
		}
	}
	if kind, err := InspectBackup(src); err != nil || kind.Encrypted {
		t.Fatalf("plain file: %+v, %v", kind, err)
	}
}
