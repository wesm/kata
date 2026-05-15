# Kata federation invariants

These invariants exist so a Phase-2 sync engine (multiple Kata daemons,
each accepting local writes, periodic merge with a hub) can plug in
without a schema rewrite. Any change to mutation paths in this codebase
must preserve them.

1. **Every state change emits an operation event sufficient for audit,
   cache invalidation, and future merge logic for that operation.**
   Rows remain the canonical local projection in v1. New mutations must
   carry enough payload to replay that mutation later — `{from, to}`
   diffs for metadata patches; UID references for cross-resource links;
   deterministic identity for recurrence materialization.

2. **Globally-unique identity is by UID, never by integer ID.**
   All cross-origin references in payloads, JSONL, and SSE frames use
   UIDs. Integer IDs are local-only and may differ across replicas.

3. **Mutations are idempotent or naturally-converging:**
   - Create — normal: server-generated UID; collision treated as
     impossible/corrupt (500). Sync replay only: same UID idempotent if
     content matches, conflict event if not.
   - Close-done — first-close wins; subsequent are audit no-ops.
   - Metadata patches (new) — per-key LWW resolvable from the
     `{from, to}` diff payload.
   - Existing field edits (title/body/priority/owner via `issue.updated`
     with empty payloads) — row-level last-write-wins by mutation
     timestamp in v1; Phase-2 per-field LWW requires expanding the
     existing event payloads to carry `{from, to}` diffs (future PR).
   - Comments / labels / links — append/set semantics; idempotent.
   - Recurrence materialization — `(recurrence_id, occurrence_key)`
     UNIQUE catches races; the loser becomes a
     `recurrence.materialization_skipped` audit event.

4. **Origin attribution preserved on every event.** New handlers set
   `origin_instance_uid` and `origin_seq`.

5. **`revision` is a local cache-coherency hint, not global state.**
   Used for `If-Match` within an origin. Don't expose its value across
   origin boundaries except as an opaque ETag string.

6. **No destructive operations that drop events.** Soft-delete by
   default; `purge` already has its own audit event.

7. **`events.origin_seq` is a real column.** Locally-created events:
   `origin_seq = events.id`. Imported events (Phase 2): preserve source
   `origin_seq`. `UNIQUE(origin_instance_uid, origin_seq)`. SSE frames
   carry both `event_id` (existing hub cursor) and `origin_seq`
   (future-compat).

8. **Event payloads use UIDs for durable references; short IDs appear
   only as display snapshots.**

9. **Conflict ordering** uses `(created_at, origin_instance_uid,
   origin_seq)` with event UID as final tie-break. Never timestamp-alone.

10. **Recurrence edits affect future instances only.** Materialized
    instances keep their historical template snapshot — template fields
    are copied into the issue at materialization time, not re-read live.

## What v1 does NOT build

- Sync protocol beyond the existing SSE.
- Local write queue.
- Vector clocks / causal-order metadata in events.
- Cross-origin schema migrations.
- Conflict-surfacing UI in clients.

## Adding a new mutation

Before merging a new mutation endpoint:

- [ ] The endpoint emits at least one event.
- [ ] The event payload contains enough information to replay the
      mutation on a different replica (where applicable).
- [ ] All cross-resource references in the payload use UIDs.
- [ ] The event handler sets `origin_seq = events.id` inside the same
      transaction as the insert.
- [ ] The endpoint is documented in `docs/superpowers/specs/` if it
      introduces a new mutation type.
