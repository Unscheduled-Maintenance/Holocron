# 8. Encrypted backups, unlocked like sync

Date: 2026-10-08

## Status

Accepted. Tracked in #30. The threat model is in
[docs/security.md](../security.md).

## Context

The archive is a plain SQLite file (ADR 0001). ADR 0007 kept it unencrypted
and encrypted only the sync folder. An encrypted archive would protect only a
disk read without its encryption, which full-disk encryption already covers;
anything running as the user could read the key from the keychain anyway
(docs/security.md).

Backups are different. `holocron backup -o /mnt/usb/` exists so that a copy
can leave the computer: a USB drive, a cloud backup folder, another machine.
Disk encryption no longer protects that copy, and it holds every entry.

The sync work already provides age encryption, an OS keychain cache and four
ways to unlock: the key itself, `sync.key_command`, a passphrase and an SSH
key.

## Options considered

1. **Encrypt only to the sync key.** No prompt, since the keychain has it.
   But it needs sync to be set up, and losing the sync key loses the backups
   too.
2. **Encrypt only to a passphrase or SSH key.** Independent of sync, but
   someone who already syncs has to manage a second secret.
3. **Choose per backup from all of them** (chosen). Each backup names who
   can open it, and restore accepts the same unlock methods as sync.
4. **Encrypt the archive itself.** Rejected for the reasons in
   docs/security.md.

## Decision

- `holocron backup` encrypts with [age](https://age-encryption.org) when
  given `--passphrase`, `--ssh-key PUB` (repeatable) or `--sync-key`, or when
  `backup.encrypt_to` lists `"passphrase"`, `"sync-key"` or SSH public key
  files. `--no-encrypt` skips the config default once.
- age imposes two limits, which are checked up front with a clear error:
  - a passphrase (scrypt) cannot be combined with any other recipient;
  - the sync key (post-quantum hybrid) cannot be combined with SSH keys
    (classic).
- The copy is made and integrity-checked unencrypted in the backups folder
  next to the archive, encrypted to its destination, and the plain copy is
  removed. Default names end in `.db.age`. The file is the standard age
  format, so `age -d` can open it without Holocron.
- `holocron restore` recognises an age file from its header and reads which
  kinds of recipient it has.
  - Passphrase: it asks for the passphrase.
  - Sync key: it tries every sync key in this computer's keychain, including
    keys replaced by `sync key rotate`, then `sync.key_command`. `--key` and
    `--key-command` are also available.
  - SSH key: it needs `--ssh-key` with the private key file.

  The decrypted copy goes to the backups folder and is removed after the
  restore.
- Automatic safety backups (before a migration, a restore or `import json`)
  stay unencrypted. They sit next to the archive, and they must restore
  without a prompt.
- `holocron doctor` counts `.db.age` files as backups.

## Consequences

- A backup on removable or cloud storage can hold confidential notes safely.
- Losing whatever opens a backup means losing that backup. The help text says
  to keep the passphrase or key somewhere other than the backup.
- Backups encrypted to the sync key open on any synced computer without a
  prompt. After a key rotation they still open on computers that kept the old
  key, and anywhere else with `--key` and the old key.
- Restoring from a sync-key backup on a fresh computer needs the sync key,
  from the password manager or `sync.key_command`, before sync is joined.
- For a moment, the backups folder holds an unencrypted copy during both
  backup and restore. It is protected like the archive itself.
