# Security and threat model

What Holocron protects, from whom, and what it leaves to the operating
system. The decisions behind this are in
[ADR 0006](adr/0006-ai-provider-boundary.md) (AI),
[ADR 0007](adr/0007-multi-device-sync.md) (sync) and
[ADR 0008](adr/0008-encrypted-backups.md) (backups).

## What is worth protecting

- **Entries.** Work notes are often confidential: incidents, people,
  customers, unreleased plans.
- **The sync key.** With it, anyone holding a copy of the sync folder can
  read every entry in it.
- **The AI provider's API key.** It is read from an environment variable and
  is never stored by Holocron.

## Where your entries are

| Copy | Where | Protected by |
|---|---|---|
| The archive (`holocron.db`, with `-wal` and `-shm` while open) | the data directory | file permissions (0600, or your profile's ACLs on Windows), plus disk encryption if you use it |
| Automatic backups (before a migration, a restore or `import json`) | `backups/` in the data directory | the same as the archive |
| `holocron backup` | the backups folder, or wherever `--output` points | the same as the archive; **encrypted** with `--passphrase`, `--ssh-key`, `--sync-key` or `backup.encrypt_to` |
| Exports and saved reports | wherever you write them | file permissions (0600) only |
| Text being edited | a `holocron-*.md` file in the system temp folder, removed afterwards | file permissions (0600) |
| The sync folder | the folder you chose, and the service that copies it | age encryption to the sync key |
| AI requests | the configured provider | sent only with `--ai` and your confirmation, and only the entries that report selected |

## Threats

| Who or what | Archive | Encrypted backup | Sync folder |
|---|---|---|---|
| The cloud service holding the sync folder, or anyone who gets into that account | not there | safe | safe |
| Someone who finds a lost USB drive, or reads a cloud backup or an emailed file | not there | safe | safe |
| A thief with a laptop whose disk is encrypted, when it is off or locked | safe (disk encryption) | safe | safe |
| A thief with a laptop whose disk is **not** encrypted | **readable** | safe | safe |
| Another, non-admin account on the same computer | safe (permissions) | safe | safe |
| An administrator, or malware running as you | **readable** | **readable**: the sync key is in your keychain | **readable**: the same |

## Decisions

**The archive itself is not encrypted.** Holocron has to open it without
asking every time, so any key would sit in the OS keychain. Anything running
as you can read the keychain. Encrypting the archive would therefore stop
only someone who has the disk without its encryption. Full-disk encryption
(BitLocker, FileVault, LUKS) covers that case, and also covers the write-ahead
log, temp files and everything else on the disk. Encrypting the archive would
also mean leaving the pure-Go SQLite driver (ADR 0001), and losing the key
would lose the journal. If your entries are sensitive, turn on disk
encryption.

**Copies that leave the computer are encrypted.** The sync folder always is
(ADR 0007). Backups are when you ask (ADR 0008), because a backup is the copy
most likely to end up on a USB drive or in someone else's cloud. Automatic
safety backups stay unencrypted: they sit next to the archive, so encrypting
them would protect nothing the archive doesn't already expose, and they must
restore without a prompt.

**Nothing is sent anywhere unless you ask.** There is no telemetry, update
check or crash reporting. AI requests need `--ai` and a confirmation each
time. Git import uses your local `git` only.

**Secrets are never written in plain text by Holocron.** The sync key is kept
in the OS keychain, or asked for again in each process when there is no
keychain. API keys come only from environment variables. Diagnostics and
error messages never include entry text.

## Not covered

- Malware or another person using your logged-in account, and
  administrators of the computer. They can read whatever you can.
- Exports and saved reports. Treat them like any document you write; encrypt
  them yourself, or keep them on an encrypted disk.
- Your terminal's scrollback and your editor's own swap or backup files.
- While `holocron backup` encrypts and `holocron restore` decrypts, an
  unencrypted copy exists briefly in the backups folder next to the archive.
  It is removed straight away and is protected the same way as the archive.
