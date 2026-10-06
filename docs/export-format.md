# Export and JSON formats

Holocron's JSON outputs are versioned by a `format` field. Within a version,
fields are only ever *added*; removing a field or changing its meaning
requires a new version string. Consumers should ignore fields they do not
recognise.

All timestamps are RFC 3339 in UTC unless the field name says otherwise.

`holocron import json FILE` reads `holocron.export/v1` files back, merging them
into an archive by UID (see the README). It uses `uid`, `ref` (or `id` for
files without one), the entry fields, `resolved_by`, and projects by name and
UID; `repository_paths` are not imported.

## Archive export — `holocron.export/v1`

Produced by `holocron export --format json`.

```json
{
  "format": "holocron.export/v1",
  "exported_at": "2026-10-05T09:35:45.421Z",
  "description": "project aws; since 2026-09-06",
  "projects": [
    {
      "uid": "01M45PNZRGYGMGFB91VBFVP4A0",
      "name": "AWS",
      "description": "Cloud accounts",
      "aliases": ["amazon"],
      "repository_paths": [],
      "urls": [],
      "archived_at": null,
      "created_at": "2026-10-05T09:35:23.92Z",
      "updated_at": "2026-10-05T09:35:23.92Z"
    }
  ],
  "entries": [ { "...": "see Entry below" } ]
}
```

| Field | Meaning |
|---|---|
| `description` | The filters used to select the entries ("all entries" when unfiltered). |
| `projects` | Every project when unfiltered; otherwise the projects the exported entries use. |
| `entries` | Oldest first. |

## Entry

The same shape is used by the export and by every command's `--json` output.

```json
{
  "id": 12,
  "ref": "#12",
  "uid": "01M45PNZRGYNXT02P679JQ1C76",
  "occurred_at": "2026-10-04T20:14:00Z",
  "recorded_local_time": "2026-10-05T09:14:00+13:00",
  "body": "Enabled IAM Access Analyzer in all AWS regions",
  "type": "accomplishment",
  "project": "AWS",
  "tags": ["iam", "security"],
  "marks": ["staff"],
  "resolved_at": null,
  "resolved_by": null,
  "resolves": [],
  "created_at": "2026-10-04T20:14:05.120Z",
  "updated_at": "2026-10-04T20:20:11.003Z",
  "source": null
}
```

| Field | Type | Meaning |
|---|---|---|
| `id` | integer | This archive's internal number for the entry, unique within the file; other fields (`resolved_by`, `resolves`) refer to it. Equal to the number in `ref` unless sync has given the entry a device label. |
| `ref` | string | The reference people see and type: `#12`, or `#12a` for an entry numbered by device `a` when sync is used ([ADR 0007](adr/0007-multi-device-sync.md)). Never reused. Added in Holocron 0.4. |
| `uid` | string | ULID; globally unique and stable across exports. |
| `occurred_at` | timestamp | When the work happened. |
| `recorded_local_time` | string | The same instant in the UTC offset where it was captured. |
| `body` | string | Entry text; may contain newlines. |
| `type` | string or null | `work`, `accomplishment`, `decision`, `investigation`, `problem`, `follow-up`, `note`. |
| `project` | string or null | Project name at export time. |
| `tags` | array of strings | Lower-case, sorted. |
| `marks` | array of strings | `staff`, `one-on-one`, `quarterly`, `important`, `cross-team`. |
| `resolved_at` | timestamp or null | Set when a problem or follow-up was resolved. |
| `resolved_by` | integer or null | The `id` of the entry that resolved this one (`holocron add --resolves`), when linked. Added in Holocron 0.3. |
| `resolves` | array of integers | The `id`s of entries this one resolved, sorted. Empty when none. Added in Holocron 0.3. |
| `created_at`, `updated_at` | timestamp | Record bookkeeping. |
| `source` | object or null | Provenance for imported entries: `{"type": "git", "id": "<commit hash>", "url": "...", "imported_at": "..."}`. `url` is omitted when unknown. |

## Report — `holocron.report/v1`

Produced by `holocron report <kind> --format json`.

```json
{
  "format": "holocron.report/v1",
  "kind": "staff",
  "title": "Staff update — this week",
  "range": {"start": "2026-10-04T11:00:00Z", "end": "2026-10-11T11:00:00Z", "label": "this week"},
  "generated_at": "2026-10-07T04:00:00Z",
  "summary": ["Left out 4 entries of routine work; include one with `holocron mark <id> staff`."],
  "sections": [
    {
      "key": "completed",
      "title": "Completed",
      "items": [
        {
          "text": "Enabled IAM Access Analyzer in all AWS regions",
          "project": "AWS",
          "type": "accomplishment",
          "occurred_at": "2026-10-04T20:14:00Z",
          "entry_ids": [12],
          "entry_refs": ["#12"],
          "reasons": ["marked staff", "type accomplishment"]
        }
      ],
      "omitted": 0
    }
  ],
  "sources": [
    {"id": 12, "ref": "#12", "uid": "01M45…", "occurred_at": "2026-10-04T20:14:00Z", "project": "AWS", "type": "accomplishment", "body": "Enabled IAM Access Analyzer in all AWS regions"}
  ]
}
```

| Field | Meaning |
|---|---|
| `range.start`, `range.end` | Half-open interval `[start, end)`; `null` when unbounded. |
| `sections[].key` | Stable identifier: `completed`, `decisions`, `problems`, `upcoming`, `cross-team`, `discuss`, `wins`, `follow-ups`, `friction`, `timeline`, `open`, `themes`, or `project:<name>`. |
| `items[].entry_ids` | Every entry the item was built from (provenance), as the `id`s used in `sources`. |
| `items[].entry_refs` | The same entries as references (`#12`, `#12a`). Added in Holocron 0.4. |
| `items[].reasons` | Why the item was selected. |
| `items[].open` | `true` for unresolved problems and follow-ups (omitted otherwise). |
| `omitted`, `omitted_entry_ids` | Lower-priority entries left out of the section. |
| `sources` | Every entry referenced by any item, with its full text. |

## Markdown export

The Markdown export is meant to be read by people, not parsed. It contains a
heading, the export time and timezone, the selection, a project list, then
`## Month Year` and `### Weekday D Month YYYY` sections. Each entry is a list
item:

```markdown
- **09:14** Enabled IAM Access Analyzer in all AWS regions  
  `#12` · project: AWS · type: accomplishment · `#iam` `#security` · marks: staff
```

Markdown control characters in entry text are escaped so that text is shown
as written.
