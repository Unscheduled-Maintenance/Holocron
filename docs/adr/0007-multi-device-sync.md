# 7. Multi-device sync through an encrypted folder

Date: 2026-10-06

## Status

Accepted. Amends [0002](0002-identifiers.md) (entry numbers). Tracked in #16.

## Context

People use Holocron on more than one computer, typically a personal and a
work laptop. The motivating case: the two are never used at the same time,
each is shut down fully before switching, and either may be offline for a
while. Moving data must not need a manual export and import, offline work
must sync once a connection returns, and conflicts must resolve without
asking.

What Holocron already has:

- Every entry and project has a ULID (0002), stable across archives and
  carried in the JSON export.
- Entry numbers (`#42`) come from a per-archive `AUTOINCREMENT` counter.
  Two archives that both add entries offline would both issue `#43`.
- Deletes remove rows outright. `resolved_by` (#9) and `project_id` link
  rows by local integer IDs.
- The archive is a single local SQLite file using a pure-Go driver (0001).
  Nothing leaves the machine except opt-in AI requests (0006).

Measured on the 15,000-entry benchmark archive (about five years of heavy
use): loading and serialising every record takes about 100 ms and compresses
to 0.67 MB. Compressing takes 32 ms, encryption is under 1 ms, and reading
and diffing another device's full copy takes about 50 ms. Process start is
about 68 ms.

## Options considered

1. **Sync the database file** with OneDrive, Dropbox or similar. This needs
   no code, but it fails the requirements. Offline changes on both computers
   produce two divergent copies, which the sync tool either overwrites or
   saves as a "conflicted copy" for a person to merge. A SQLite file copied
   mid-write can also be inconsistent.
2. **A CRDT SQLite extension** (cr-sqlite). It gives automatic merging, but
   it needs CGO and a native extension per platform, which reverses 0001
   and the single static binary.
3. **A server or hosted replica** (a Holocron service, Turso/libSQL). This
   adds an account, a running service and a third party holding the data,
   which is contrary to local-first. Offline writes are also the hard part
   for these systems.
4. **Each device publishes its own encrypted change files to a shared
   folder, and every device merges everyone else's.** The folder can be
   anything that moves files: OneDrive, Dropbox, iCloud Drive, a network
   share. *Chosen.*

Within option 4, entry numbering was considered separately:

- *Numbers stay per device* (`#43` means different entries on each laptop).
  This is simple, but a written `#42` stops identifying one entry.
- *Interleaved ranges* (one device odd, the other even). Numbers are unique,
  but they look arbitrary and don't scale past two devices.
- *A device prefix* (`#a12`). It reads well, but `#` followed by a letter
  is tag shorthand, so `#a12` typed into an entry would become a tag.
- *A device suffix* (`#12a`). `#` followed by a digit is never a tag, and
  the reference still reads as "entry 12". *Chosen.*

## Decision

### Devices and entry numbers

- Enabling sync gives the computer a **device**: a ULID and a **label**.
  Labels are single lower-case letters assigned automatically in order
  (`a`, `b`, `c`, …) by reading the devices already in the sync folder.
  A label is fixed once assigned, because renaming would change every
  reference already written.
- Each device numbers its own new entries from its own counter. They are
  shown and accepted as `#12a`. The internal `INTEGER` primary key stays,
  for foreign keys and indexes, but is no longer shown for these entries.
- **Existing entries keep their plain numbers** (`#42`) everywhere, so
  every reference written before sync keeps its meaning. To keep numbers
  distinct from them, every device's counter starts after the highest plain
  number in the archive at the time sync is enabled.
- Commands accept `#12a`, `12a`, a ULID, or a plain number. A plain number
  in the old range means that old entry. A plain number above it means this
  device's own entry (`show 50` on device `a` is `#50a`). Output always
  prints the suffix, so a copied reference is never ambiguous.
- Joining requires reading the sync folder, so labels can't be chosen
  blind. If two devices still end up with the same label (for example a
  file not yet downloaded), the device that registered later stops syncing.
  It then asks for `holocron sync relabel`, which renumbers that device's
  own entries.

### What syncs

These sync:

- entries;
- projects, with their aliases, links and archived state;
- report marks;
- `--resolves` links;
- the report log, so `--since last` works across devices.

These don't sync:

- configuration, including type aliases, editor and clock;
- backups;
- local caches.

Each laptop keeps its own config.

### Merging

- Each record is identified by its ULID. References between records (an
  entry's project, `resolved_by`) are written as ULIDs in sync files and
  translated to local IDs on arrival.
- Every change carries a **hybrid logical clock** (HLC): wall-clock time
  plus a counter, never moving backwards, and never less than any clock
  seen from another device. A laptop whose clock is ahead therefore can't
  win every conflict.
- **Per-field last-writer-wins.** An entry's fields merge independently,
  and the highest HLC wins. Ties go to the lower device ULID. The fields
  are:
  - text;
  - time, including its UTC offset;
  - type;
  - project;
  - tags (as a set);
  - marks (as a set);
  - resolution (`resolved_at` and `resolved_by` together).

  Import provenance (`source`) never changes after creation. Editing the
  tags on one device and the text on the other keeps both edits.
- **Deletes leave a tombstone** (ULID, HLC, device). A tombstone beats any
  edit with a lower HLC. Undoing a delete writes a newer "restored" record,
  so an undo also syncs. Tombstones are kept indefinitely: they are tiny,
  and dropping them would let an old copy resurrect a deleted entry.
- **Projects with the same name but different ULIDs** (created separately
  while offline) merge into the one with the lower ULID. The other ULID is
  kept as a redirect, so entries referring to it follow. Project aliases
  stay unique: if two devices give the same alias to different projects,
  the later change wins, and `holocron doctor` reports the alias the other
  project lost.
- The merge is deterministic and idempotent: applying the same files again,
  or in a different order, gives the same archive.

### The sync folder

```
<folder>/holocron-sync/
  format.json                     # sync format version (not secret)
  keys/                           # the data key, wrapped once per unlock method
    <method>-<id>.age
  devices/<device-ulid>/
    device.age                    # label, display name, created
    snapshot-<hlc>.age            # full state of this device's view
    changes-<hlc>.age             # records changed since the previous file
```

- **Each device writes and deletes only files in its own directory.** No
  file is ever edited by two devices, so the sync tool never sees a
  conflict.
- Files are written under a temporary name and renamed when complete, so a
  half-written file is never uploaded.
- **After every change**, the device writes a small `changes-` file holding
  only the records that changed. That costs about 1 ms and a few hundred
  bytes.
- **Occasionally** (daily, or after about 50 change files), the device
  writes a fresh full `snapshot-` file. Only after that does it delete its
  own older snapshot and change files. That costs about 130 ms at 15,000
  entries. `holocron sync compact` does it on demand.
- **At the start of every command**, Holocron lists the other devices'
  directories and compares them with a per-device cursor: the name of the
  last file it read. When nothing is new, that costs about 1 ms. Otherwise
  it reads the new files. If its cursor's file was compacted away, it reads
  the newer snapshot instead.
- **The TUI** pulls when it starts and on refresh, and pushes after each
  change.
- **If the folder is missing, a file is cloud-only or unreadable, or the
  key can't be unlocked**, Holocron keeps working locally and warns once.
  The skipped files are retried on the next command. `holocron sync
  status` and `holocron doctor` show:
  - what is pending;
  - when each device last published;
  - whether this device's latest changes have been written to the folder.

  Holocron can't see whether OneDrive has uploaded them yet.
- A `format.json` version newer than this Holocron understands stops sync
  with a message to upgrade. Older formats are read.

### Encryption and keys

- Every file except `format.json` is encrypted with
  [age](https://age-encryption.org) (`filippo.io/age`, pure Go). age's
  authenticated encryption also rejects altered or damaged files.
- `holocron sync init` generates one random **data key** (an age
  identity; implemented with age's post-quantum hybrid key, its recommended
  native key since v1.3), and every sync file is encrypted to it. The data key itself is
  stored only in wrapped form, once per unlock method chosen. A device
  unlocks it once, by any one of those methods, and caches it in the
  **OS keychain**:
  - Windows Credential Manager;
  - macOS Keychain;
  - the Secret Service on Linux.

  It uses `github.com/zalando/go-keyring`, which needs no CGO. Normal
  commands only read the cache.

| Unlock method | How | Notes |
|---|---|---|
| Key | `sync init` prints the data key once; `sync join` asks for it. | Keep it in a password manager. No wrapped file is needed. |
| `key_command` | `[sync] key_command = "op read op://Private/Holocron/sync-key"`. Its output is the data key. | Works with 1Password, `pass`, and similar tools. It runs only when the cache is empty. |
| Passphrase | `keys/passphrase-*.age`, an age scrypt recipient. | Deliberately slow (about a second), so it is paid only once per device. age requires a passphrase file to have no other recipients, hence one file per method. |
| SSH key | `keys/ssh-*.age`, an age SSH recipient (Ed25519 or RSA). | Decryption needs the private key file. Agents that never release the private key, such as 1Password's, can't be used for this method. |

- **When no keychain is available** (for example a Linux system without a
  Secret Service), Holocron asks for the key again (or runs `key_command`)
  in each process and says why. It never writes the data key to a plain
  file.
- **`holocron sync rotate-key`** creates a new data key, re-wraps it for the
  chosen methods and writes a new snapshot. Files record which key they use.
  A device that hasn't unlocked the new key gets a clear error, not silent
  failure.
- The local archive stays unencrypted, as today. This protects the copy held
  by the cloud provider, not the laptop.

### Implementation order

1. **Schema groundwork, with no sync yet:**
   - a `devices` table and each entry's device and number;
   - per-field HLCs;
   - tombstones;
   - ULIDs for report-log rows;
   - parsing and printing `#12a`.

   JSON import (#11) builds on this merge engine, treating an export file
   as a one-off remote device whose clocks are its `updated_at` times.
2. **Sync:**
   - `holocron sync init` / `join` with the key method and the OS keychain;
   - change files, snapshots, compaction and cursors;
   - pull on every command, push after every change.
3. **The remaining unlock methods:** passphrase, SSH, and `key_command`.
4. **Reporting and docs:**
   - `holocron sync status`;
   - `doctor` checks;
   - a sync section in the README and `docs/sync-format.md`.

## Consequences

- Each laptop works fully offline and catches up on its next command once
  the other's files arrive, with no export, import or merge step.
- Conflicts resolve automatically and predictably. The rule is "newest
  change to each field wins, and deletes stick unless undone", and it can
  be explained in one sentence.
- **Entry numbers change form.** New entries are `#12a`, old ones stay
  `#42`. A plain number above the old range means "this device's", which
  is convenient to type but means the same keystrokes name different
  entries on different devices. Printed references always include the
  suffix.
- **Schema and storage grow:**
  - a device label and number per entry;
  - clocks for each field;
  - tombstones forever;
  - one wrapped key file per unlock method.

  These are small next to the entries themselves.
- **New dependencies:** `filippo.io/age` and `github.com/zalando/go-keyring`.
  Both are pure Go, so the single static binary remains.
- **Sync files reveal some metadata** to whoever holds the folder:
  - how many devices there are;
  - when each one last changed something;
  - roughly how much data there is.

  They don't reveal contents. Deleting or replaying old files in the folder
  can delay sync but can't inject changes, and the local archives remain
  the source of truth.
- **People must check policy themselves.** Syncing work notes through a
  personal cloud folder may be against an employer's rules. The README must
  say so plainly.
- A change made just before a laptop is shut down offline reaches the other
  laptop only after the first is next online. `sync status` makes this
  visible but can't prevent it.
- Holocron gains a sync format to version and document alongside
  `holocron.export/v1`.
