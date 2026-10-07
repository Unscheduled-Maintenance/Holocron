package devsync

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"filippo.io/age"
	"filippo.io/age/agessh"
)

// Backups made with `holocron backup` can be age-encrypted, to recipients
// chosen per backup: a passphrase, SSH public keys or the sync key. They are
// unlocked the same ways sync is. See docs/adr/0008-encrypted-backups.md.

const ageMagic = "age-encryption.org/v1\n"

// BackupLock chooses who can open an encrypted backup. A passphrase cannot
// be combined with anything else, and the sync key (post-quantum) cannot be
// combined with SSH keys (classic): both are age restrictions.
type BackupLock struct {
	Passphrase    string
	SSHPublicKeys []string // authorized_keys lines, as in id_ed25519.pub
	SyncKey       bool     // the current sync key
}

// Empty reports whether no recipient was chosen.
func (l BackupLock) Empty() bool {
	return l.Passphrase == "" && len(l.SSHPublicKeys) == 0 && !l.SyncKey
}

func (s *Syncer) backupRecipients(ctx context.Context, l BackupLock) ([]age.Recipient, error) {
	if l.Passphrase != "" {
		if len(l.SSHPublicKeys) > 0 || l.SyncKey {
			return nil, errors.New("a passphrase-protected backup cannot also be opened with an SSH key or the sync key; choose one")
		}
		if len(l.Passphrase) < 8 {
			return nil, errors.New("use a passphrase of at least 8 characters")
		}
		r, err := age.NewScryptRecipient(l.Passphrase)
		if err != nil {
			return nil, err
		}
		r.SetWorkFactor(ScryptWorkFactor)
		return []age.Recipient{r}, nil
	}
	if l.SyncKey && len(l.SSHPublicKeys) > 0 {
		return nil, errors.New("a backup encrypted to the sync key cannot also be opened with an SSH key (age does not mix post-quantum and classic keys); choose one")
	}
	var rs []age.Recipient
	for _, pub := range l.SSHPublicKeys {
		r, err := agessh.ParseRecipient(strings.TrimSpace(pub))
		if err != nil {
			return nil, fmt.Errorf("not a usable SSH public key (ssh-ed25519 or ssh-rsa): %w", err)
		}
		rs = append(rs, r)
	}
	if l.SyncKey {
		cfg, err := s.Configured(ctx)
		if errors.Is(err, ErrNotConfigured) {
			return nil, errors.New("sync is not set up, so there is no sync key to encrypt the backup to")
		}
		if err != nil {
			return nil, err
		}
		ff, err := folderAt(cfg.Folder).readFormat()
		if err != nil {
			return nil, err
		}
		key, err := s.currentKey(ff)
		if err != nil {
			return nil, err
		}
		rs = append(rs, key.Recipient())
	}
	if len(rs) == 0 {
		return nil, errors.New("choose who can open the backup: a passphrase, an SSH key or the sync key")
	}
	return rs, nil
}

// EncryptBackup encrypts the file at src to dst, which must not exist.
func (s *Syncer) EncryptBackup(ctx context.Context, src, dst string, l BackupLock) error {
	rs, err := s.backupRecipients(ctx, l)
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	w, err := age.Encrypt(out, rs...)
	if err != nil {
		return fail(err)
	}
	if _, err := io.Copy(w, in); err != nil {
		return fail(err)
	}
	if err := w.Close(); err != nil {
		return fail(err)
	}
	if err := out.Sync(); err != nil {
		return fail(err)
	}
	return out.Close()
}

// BackupKind describes how a backup file is protected.
type BackupKind struct {
	Encrypted  bool
	Passphrase bool // opened with a passphrase
	SSH        bool // opened with an SSH private key
	Key        bool // opened with a sync key
}

// InspectBackup reports whether a file is age-encrypted, and to what.
func InspectBackup(path string) (BackupKind, error) {
	var k BackupKind
	f, err := os.Open(path)
	if err != nil {
		return k, err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	magic := make([]byte, len(ageMagic))
	if _, err := io.ReadFull(r, magic); err != nil || string(magic) != ageMagic {
		return k, nil
	}
	k.Encrypted = true
	for {
		line, err := r.ReadString('\n')
		if strings.HasPrefix(line, "---") || err != nil {
			return k, nil
		}
		fields := strings.Fields(strings.TrimPrefix(line, "-> "))
		if !strings.HasPrefix(line, "-> ") || len(fields) == 0 {
			continue
		}
		switch t := fields[0]; {
		case t == "scrypt":
			k.Passphrase = true
		case strings.HasPrefix(t, "ssh-"):
			k.SSH = true
		case t == "mlkem768x25519":
			k.Key = true
		}
	}
}

// DecryptBackup decrypts an encrypted backup at src to dst, which must not
// exist. With an empty Unlock it tries the sync keys this computer has, then
// sync.key_command.
func (s *Syncer) DecryptBackup(ctx context.Context, src, dst string, u Unlock) error {
	ids, err := s.backupIdentities(ctx, u)
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	r, err := age.Decrypt(in, ids...)
	if err != nil {
		var noMatch *age.NoIdentityMatchError
		if errors.As(err, &noMatch) {
			return fmt.Errorf("that does not unlock %s", src)
		}
		return fmt.Errorf("decrypting %s: %w", src, err)
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return fmt.Errorf("decrypting %s (damaged or altered?): %w", src, err)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}

func (s *Syncer) backupIdentities(ctx context.Context, u Unlock) ([]age.Identity, error) {
	switch {
	case u.Key != "":
		id, err := parseDataKey(u.Key)
		return []age.Identity{id}, err
	case u.Passphrase != "":
		id, err := age.NewScryptIdentity(u.Passphrase)
		return []age.Identity{id}, err
	case u.SSHKey != "":
		id, err := sshIdentity(u.SSHKey, u.SSHPassphrase)
		return []age.Identity{id}, err
	case u.KeyCommand:
		out, err := runKeyCommand(s.KeyCommand)
		if err != nil {
			return nil, err
		}
		id, err := parseDataKey(out)
		return []age.Identity{id}, err
	}
	// Every sync key this computer keeps, including ones replaced by a
	// rotation, so older backups still open.
	if s.Store != nil {
		if cfg, err := s.Configured(ctx); err == nil {
			if ids, err := s.identitiesVia(cfg.Set, false); err == nil {
				return ids, nil
			}
		}
	}
	if len(s.KeyCommand) > 0 {
		return s.backupIdentities(ctx, Unlock{KeyCommand: true})
	}
	return nil, errors.New("this backup is encrypted to a sync key this computer does not have; pass --key, or set sync.key_command")
}
