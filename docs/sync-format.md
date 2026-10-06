# Sync folder format

This describes what Holocron writes to a sync folder (`holocron sync init`).
[ADR 0007](adr/0007-multi-device-sync.md) records why it works this way. The
format is versioned by `format.json`. A Holocron that finds a version it does
not know stops syncing and asks to be upgraded.

## Layout

```
<chosen folder>/holocron-sync/
  format.json
  keys/
    passphrase.age
    ssh-<fingerprint>.age
  devices/<device-uid>/
    device.age
    snapshot-<stamp>.age
    changes-<stamp>.age
```

- **Each computer writes and deletes only files in its own
  `devices/<device-uid>/` directory**, so a sync service never sees two
  computers edit the same file.
- **Files are written under a temporary name** (`.tmp-…`) and renamed when
  complete, so a half-written file is never published.
- **`<stamp>` is milliseconds since 1970, zero-padded to 16 digits.** It
  orders one computer's files and always increases.
- **A snapshot holds everything that computer knows.** A changes file holds
  the records it changed since its previous file.
- **A receive-only computer writes only `device.age`**, so its label stays
  taken. It removes any snapshot and changes files of its own.

## format.json

Not encrypted:

```json
{
  "format": "holocron.sync/v1",
  "set": "01M488G9TTE7XJCG4TZKZ24T70",
  "key_id": "3f9a1c0b7e2d",
  "created": "2026-10-06T09:25:20Z"
}
```

| Field | Meaning |
|---|---|
| `set` | ULID of the sync set; an archive syncs with one set. |
| `key_id` | First 12 hex digits of SHA-256 of the current key's public recipient. Not secret. A computer whose key has a different ID must unlock again. |

## Encryption

**The sync key.** Every other file is an [age](https://age-encryption.org)
file encrypted to the sync key: a post-quantum hybrid age identity
(`AGE-SECRET-KEY-PQ-1…`).

**The `keys/` files** each hold that identity as text, encrypted for one
unlock method:

- `passphrase.age` uses an age scrypt recipient.
- `ssh-<fingerprint>.age` uses an age SSH recipient (`ssh-ed25519` or
  `ssh-rsa`). The fingerprint is the first 12 hex digits of SHA-256 of the
  public key.

**On each computer**, the unlocked identity is kept in the OS keychain under
the service `holocron-sync`, with the set ID as the account. After a key
rotation, older keys are kept there too until the files encrypted with them
are gone.

## device.age

JSON once decrypted:

```json
{"uid": "01M488G9TTE7XJCG4TZKZ24T70", "label": "a", "name": "work"}
```

## snapshot and changes files

Gzip-compressed JSON once decrypted. Records refer to each other by UID,
never by local row number.

```json
{
  "devices": [{"uid": "01M488G9…", "label": "a", "name": "work"}],
  "projects": [{
    "uid": "01M45…", "name": "Infra", "description": "", "aliases": ["inf"], "urls": [],
    "archived_at": null, "created_at": "…", "updated_at": "…",
    "clocks": {"name": "1791278720944.000000.01M488G9…", "aliases": "…"}
  }],
  "entries": [{
    "uid": "01M45…", "num": 2, "label": "b", "device": "01M488GA…",
    "occurred_at": "…", "utc_offset": 46800, "body": "ARM approved",
    "type": "", "project": "<project uid>", "tags": ["arm"], "marks": [],
    "resolved_at": null, "resolved_by": "<entry uid>",
    "created_at": "…", "updated_at": "…", "source": null,
    "clocks": {"body": "1791278721245.000000.01M488GA…", "tags": "…"}
  }],
  "tombstones": [{"uid": "01M45…", "kind": "entry", "clock": "…"}],
  "reports": [{"uid": "…", "kind": "staff", "range_start": "…", "range_end": "…", "recorded_at": "…"}]
}
```

| Field | Meaning |
|---|---|
| `num`, `label`, `device` | The entry's number. With a label it is shown as `#2b`; `device` is the UID of the computer that issued the number. Without a label it is a plain number from before sync. |
| `clocks` | When each field last changed, as a hybrid logical clock: `<unix ms, 13 digits>.<counter, 6 digits>.<device uid>`. Entry fields: `body`, `time` (with `utc_offset`), `type`, `project`, `tags`, `marks`, `resolved` (with `resolved_by`). Project fields: `name`, `description`, `archived`, `aliases`, `urls`. |
| `tombstones` | Deleted entries and projects, with the clock of the delete. |
| `reports` | Reports recorded with `holocron report … --record`, so `--since last` works on every computer. |

## Merging

Merging follows the same rules as `holocron import json`:

- **Fields:** for each field, the later clock wins. A clock tie goes to the
  lower device UID.
- **Deletes:** a tombstone wins over a record whose latest clock is older,
  and a newer record removes the tombstone.
- **Projects:** projects with the same name merge into the lower UID, and
  the other UID is kept as a redirect.
- **Repeats:** applying the same file again changes nothing.

## Reading order

For each other computer, Holocron remembers the stamp of the last file it
merged. It then reads the newest snapshot after that stamp, if there is
one, and the changes files after it. A computer folds its files into a new
snapshot once a day, or after 50 changes files. It deletes its older files
only after the new snapshot is written.
