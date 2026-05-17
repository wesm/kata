# Kata federation invariants

Federation between kata instances is explicitly out of scope for v1.
v1 is single-user, single-daemon, loopback-only.

Past plans included a per-instance event sequence number (`origin_seq`)
for federation ordering, a partial unique index on
`(origin_instance_uid, origin_seq)`, version-gated v9→v10 importer
support for the field, and conflict-resolution rules built on top of
that ordering. All of that was removed before v1 ship. The git history
holds the full design (see commits `ee8b697`, `30dabf1`, `7877121`)
if federation is revisited.

What survives from the original design in v1 because it has independent
value:

- `events.uid` (ULID) and `events.origin_instance_uid` on every row.
  Event identity in v1 is `(event_id, origin_instance_uid)`. The
  daemon stamps `origin_instance_uid` on every locally-emitted event.
- UID-keyed cross-resource references in event payloads. Integer IDs
  are local-only; UIDs are stable across replays.
- Soft-delete by default; `purge` has its own audit event.
- `revision` as a local cache-coherency hint (used for `If-Match`).

When federation is designed, the right time to reintroduce per-instance
ordering, conflict rules, and cross-origin schema cutover is then —
not now.
