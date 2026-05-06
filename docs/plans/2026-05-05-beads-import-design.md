# Beads Import Design

## Issue

GitHub issue #16 requests a Beads import command. Goal: help Beads users exit by importing live Beads issues into the current kata project while preserving history enough to remain useful.

## Decisions

- CLI shape: `kata import --format beads`.
- Existing kata JSONL import remains default: `kata import --input PATH --target PATH [--force]` behaves as it does today. `--format kata` is equivalent to the default if added.
- Beads import requires a live Beads workspace. It does not accept `--input`, `--target`, `--force`, or `--new-instance`.
- CLI shells out to `bd export --no-memories` and `bd comments <id> --json`.
- Import destination is the current kata project/daemon DB, not an offline target DB.
- If no kata project is initialized:
  - interactive mode prompts to run `kata init`;
  - unattended mode errors with guidance to run `kata init` first.
- Unattended mode means any of: non-TTY stdin/stdout, `--json`, or `--quiet`.
- Preserve imported timestamps exactly.
- Import comments from Beads, not just `comment_count`.
- Reimport upserts existing imported issues.
- Use source timestamps to decide whether source or local issue fields win.
- Add a generic `import_mappings` table so issues and comments retain source identity.
- Build a generic daemon import endpoint so future importers can reuse the same path.

## Architecture

Add a generic daemon endpoint:

```text
POST /api/v1/projects/{project_id}/imports
```

The request is a normalized import model rather than raw Beads data:

```text
actor
source
items[]
  external_id
  title
  body
  author
  owner
  status
  closed_reason
  created_at
  updated_at
  closed_at
  labels[]
  comments[]
    external_id
    author
    body
    created_at
  links[]
    type
    target_external_id
  metadata
```

The daemon owns persistence, source identity mapping, upsert conflict handling, events, hooks, and SSE broadcast. The CLI owns source-specific collection and translation.

## CLI Flow

For `kata import --format beads`:

1. Reject incompatible flags: `--input`, `--target`, `--force`, `--new-instance`.
2. Resolve workspace from `--workspace` or current directory.
3. Resolve current kata project.
4. If project resolution fails because kata is not initialized:
   - when interactive and not `--json`/`--quiet`, prompt: `No kata project found. Run kata init now? [y/N]`;
   - on yes, run existing init flow, then continue;
   - on no or unattended mode, return validation error: `run kata init first`.
5. Find `bd` on `PATH`.
6. Run `bd export --no-memories` in the workspace.
7. For each exported issue, run `bd comments <id> --json` in the workspace.
8. Convert Beads records to the normalized import request.
9. POST to the daemon import endpoint.
10. Human output: `imported beads: created N, updated M, unchanged K, comments C, links L`.
11. JSON output: emit daemon response body.

If `bd` is missing, return a validation error telling the user to install Beads or add `bd` to `PATH`. If Beads commands fail, surface their stderr in a concise import error.

## Beads Mapping

For each Beads issue record:

| Beads | Kata import model |
| --- | --- |
| `id` | `external_id` |
| `title` | `title` |
| `description` | base `body` |
| `created_by` | `author` fallback CLI actor |
| `owner` | `owner` |
| `status` | `status` (`open`/`closed`) |
| `close_reason` | `closed_reason` |
| `created_at` | `created_at` |
| `updated_at` | `updated_at` |
| `closed_at` | `closed_at` |
| `labels[]` | normalized labels |
| `dependencies[].depends_on_id` | `blocks` link source |
| `comment_count` | metadata/footer only |
| `priority` | metadata/footer only |
| `issue_type` | metadata/footer only |

A Beads dependency where issue `A` depends on `B` maps to kata link `B --blocks--> A`. Kata `blocks` links are directed from blocker to blocked; `kata block <blocker> <blocked>` and ready-issue filtering both use that direction.

## Metadata Labels, Close Reasons, and Footer

Each imported issue gets labels:

- `source:beads`
- `beads-id:<normalized-or-hash>`

Beads labels are normalized to kata's label constraints: lowercase, whitespace to `-`, characters outside `[a-z0-9._:-]` replaced or stripped, repeated separators collapsed, and long labels truncated with a stable hash. `-` is allowed by kata's current label constraint. Original labels are preserved in the footer.

Kata close reasons are limited to `done`, `wontfix`, and `duplicate`. Beads close reasons map directly when they match. Unsupported or empty Beads close reasons on closed issues map to `done`, with the original reason preserved in the footer.

The body footer is stable and parseable:

```text
---
Imported from Beads
beads_id: <id>
beads_type: <issue_type>
beads_priority: <priority>
beads_original_labels: ["label one", "Label Two"]
beads_created_at: <timestamp>
beads_updated_at: <timestamp>
beads_closed_at: <timestamp>
beads_close_reason: <original reason>
beads_comment_count: <n>
```

The base Beads description remains at the top of the issue body. The footer preserves metadata that has no first-class kata equivalent without cluttering kata labels.

## Source Identity and Upsert

Add a generic `import_mappings` table. It records imported object identity without polluting user-visible fields:

```text
import_mappings
  id
  source                  -- e.g. beads
  external_id             -- source object id
  object_type             -- issue|comment|label|link
  project_id
  issue_id                -- owning kata issue for issue/comment/label/link mappings
  comment_id              -- set for imported comments
  link_id                 -- set for imported links
  label                   -- set for imported labels
  source_updated_at       -- source-side updated timestamp when available
  imported_at
  UNIQUE(source, external_id, object_type, project_id)
```

The `source:beads` and `beads-id:<normalized-or-hash>` labels remain useful for humans and search, but mapping rows are authoritative for reimport.

Reimport behavior:

- If no issue mapping exists, create the issue and mapping.
- If an issue mapping exists, compare Beads `updated_at` with the kata issue `updated_at`.
  - Beads newer: update kata title, body/footer, status, owner, labels, and links from Beads; preserve exact Beads timestamps.
  - Kata same or newer: keep kata issue fields.
- Comments merge independently by Beads comment ID using `import_mappings`.
  - Missing comment mapping: create comment with Beads `created_at` and mapping.
  - Existing comment mapping: keep existing kata comment unless a future source exposes comment update timestamps.
- Labels and links use mapping rows too.
  - Label external IDs are deterministic: `<issue_external_id>:label:<normalized_label>`.
  - Link external IDs are deterministic: `<from_external_id>:<type>:<to_external_id>` after kata direction mapping.
  - When Beads is newer for the issue, source-owned labels/links are reconciled to the Beads set.
  - Non-Beads/local labels and links are untouched.

Footer parsing is fallback only for older imports created before `import_mappings` exists.

## Schema and JSONL Compatibility

Adding `import_mappings` is a schema change:

- Add a normal migration and bump the schema/export version.
- Existing DBs get the table through the same daemon startup cutover path used by other schema changes.
- Kata JSONL export/import must preserve `import_mappings`, so backup/restore keeps authoritative source identity.
- Older JSONL imports without mappings remain valid; footer fallback can recover Beads issue identity where possible, but not comment identity.

## Timestamp Fidelity

The import endpoint writes explicit timestamps:

- issue `created_at`, `updated_at`, `closed_at`;
- comments `created_at`;
- labels `created_at` where available, otherwise issue `created_at`;
- links `created_at` where available, otherwise issue `created_at`;
- import events `created_at` aligned to the imported mutation where possible.

Aside: existing kata DB mutation functions should set `updated_at` intentionally and consistently. Normal user mutations can continue to use current time. Import-specific DB methods must accept explicit timestamps instead of using SQLite `now`.

## Daemon Import Behavior

The endpoint performs all import writes for a request transactionally enough to avoid partial issue rows. Recommended behavior:

1. Validate actor, source, project, item fields, statuses, timestamps, and link targets.
2. Preload existing `import_mappings` for the source/project.
3. Create new issues first, preserving timestamps and writing issue mappings.
4. For mapped issues, compare source `updated_at` to kata `updated_at` and update only when the source is newer.
5. Insert or reconcile labels for created/source-newer issues.
6. Insert missing comments by comment mapping, preserving comment timestamps.
7. Resolve links after all issues have either been created or matched through mappings.
8. Insert or reconcile source-owned links for created/source-newer issues.
9. Emit events and broadcast/hooks for created, updated, commented, labeled/unlabeled, linked/unlinked state.
10. Return summary plus per-item created/updated/unchanged info.

Bad source data should fail with validation errors where the caller can fix input. Missing link targets should reject the request so a migration does not silently lose dependency information.

## Response Shape

Response body:

```text
source
created
updated
unchanged
comments
links
items[]
  external_id
  issue_number
  status: created|updated|unchanged
  reason
errors[]
```

The response should be stable for `--json` consumers and concise for humans.

## Testing Plan

Adapter unit tests:

- parse `bd export` JSONL;
- parse `bd comments --json`;
- normalize labels, including invalid and overlong labels;
- build footer;
- map dependencies to `blocks` links with kata blocker-to-blocked direction;
- reject unsupported Beads status values.

Daemon import endpoint tests:

- creates issues with exact `created_at`, `updated_at`, `closed_at`;
- imports labels, comments, and links;
- preserves comment timestamps;
- creates `import_mappings` for issues, comments, labels, and links;
- exports/imports `import_mappings` through kata JSONL;
- reimport upserts source-newer issues;
- reimport leaves local-newer issues unchanged while adding missing comments;
- rejects invalid source, blank actor, invalid status, and bad link target;
- broadcasts/enqueues expected events.

CLI tests:

- `kata import --format beads --input file` is rejected;
- missing kata project in unattended mode errors with `run kata init first`;
- fake `bd` in `PATH` supplies export/comments fixtures and import succeeds;
- human summary and JSON summary are stable;
- existing kata JSONL import remains compatible when no `--format` is supplied.

Existing DB behavior tests:

- create/edit/comment/label/link/close/reopen/delete/restore keep `updated_at` semantics intentional;
- import path can set explicit timestamps without SQLite `now`.

## Non-goals

- Pure JSONL Beads import through `--input`.
- Pure JSONL Beads upsert without live Beads.
- Full Beads Dolt history import.
- First-class kata fields for Beads priority/type.
- Importing Beads memories or infrastructure records.
