package cli

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func writeSSHKeyPair(t *testing.T, dir string) (pubPath, privPath string) {
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
	privPath = filepath.Join(dir, "id_ed25519")
	pubPath = privPath + ".pub"
	if err := os.WriteFile(privPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pubPath, ssh.MarshalAuthorizedKey(sp), 0o600); err != nil {
		t.Fatal(err)
	}
	return pubPath, privPath
}

func notPlain(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), "age-encryption.org/v1\n") || strings.Contains(string(raw), "SQLite format 3") {
		t.Fatalf("%s is not an encrypted backup", path)
	}
}

func TestEncryptedBackupWithPassphrase(t *testing.T) {
	h := newHarness(t)
	h.ok("add", "Secret plans")
	dest := filepath.Join(h.dir, "usb")
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	r := h.runIn("correct horse battery\n", "backup", "-o", dest, "--passphrase")
	if r.code != 0 {
		t.Fatalf("backup: %+v", r)
	}
	mustContain(t, r.out, "Backed up (encrypted)", ".db.age", "1 entries")
	files, _ := filepath.Glob(filepath.Join(dest, "holocron-*.db.age"))
	if len(files) != 1 {
		t.Fatalf("backups: %v", files)
	}
	notPlain(t, files[0])
	// No unencrypted copy is left behind.
	if left, _ := filepath.Glob(filepath.Join(h.dir, "data", "backups", ".holocron-*")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}

	h.ok("add", "After the backup")
	if r := h.runIn("wrong horse battery\n", "restore", files[0], "--force"); r.code == 0 {
		t.Fatal("restored with the wrong passphrase")
	}
	r = h.runIn("correct horse battery\n", "restore", files[0], "--force")
	if r.code != 0 {
		t.Fatalf("restore: %+v", r)
	}
	mustContain(t, r.out, "Restored 1 entries")
	list := h.ok("list")
	mustContain(t, list, "Secret plans")
	mustNotContain(t, list, "After the backup")
	if left, _ := filepath.Glob(filepath.Join(h.dir, "data", "backups", ".holocron-*")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
	mustContain(t, h.ok("doctor"), "backups")
}

func TestEncryptedBackupWithSSHKey(t *testing.T) {
	h := newHarness(t)
	h.ok("add", "Secret plans")
	pub, priv := writeSSHKeyPair(t, h.dir)
	dest := filepath.Join(h.dir, "b.db.age")
	h.ok("backup", "-o", dest, "--ssh-key", pub)
	notPlain(t, dest)
	r := h.run("restore", dest, "--force")
	if r.code != ExitUsage || !strings.Contains(r.err, "--ssh-key") {
		t.Fatalf("restore without the key: %+v", r)
	}
	mustContain(t, h.ok("restore", dest, "--force", "--ssh-key", priv), "Restored 1 entries")

	if r := h.runIn("x\n", "backup", "--ssh-key", pub, "--passphrase"); r.code == 0 {
		t.Fatal("combined a passphrase with an SSH key")
	}
	if r := h.run("backup", "--ssh-key", pub, "--no-encrypt"); r.code == 0 {
		t.Fatal("combined --no-encrypt with --ssh-key")
	}
	if r := h.run("backup", "--sync-key"); r.code == 0 || !strings.Contains(r.err, "sync is not set up") {
		t.Fatalf("--sync-key without sync: %+v", r)
	}
}

func TestEncryptedBackupWithSyncKeyByDefault(t *testing.T) {
	h := newHarness(t)
	folder := filepath.Join(h.dir, "OneDrive")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	h.ok("add", "Secret plans")
	h.ok("sync", "init", folder)
	cfg := "[backup]\nencrypt_to = [\"sync-key\"]\n"
	if err := os.WriteFile(filepath.Join(h.dir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	out := h.ok("backup")
	mustContain(t, out, "Backed up (encrypted)", ".db.age")
	files, _ := filepath.Glob(filepath.Join(h.dir, "data", "backups", "holocron-*.db.age"))
	if len(files) != 1 {
		t.Fatalf("backups: %v", files)
	}
	notPlain(t, files[0])
	// The key this computer keeps opens it without asking.
	mustContain(t, h.ok("restore", files[0], "--force"), "Restored 1 entries")

	plain := filepath.Join(h.dir, "plain.db")
	mustContain(t, h.ok("backup", "--no-encrypt", "-o", plain), "Backed up ")
	if raw, _ := os.ReadFile(plain); !strings.HasPrefix(string(raw), "SQLite format 3") {
		t.Fatal("--no-encrypt backup is not a plain archive")
	}

	bad := "[backup]\nencrypt_to = [\"sync-key\", \"~/.ssh/id_ed25519.pub\"]\n"
	if err := os.WriteFile(filepath.Join(h.dir, "config.toml"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := h.run("backup"); r.code == 0 || !strings.Contains(r.out+r.err, "cannot be combined") {
		t.Fatalf("bad encrypt_to: %+v", r)
	}
}
