# kata — agent guidance

## Project management

This project tracks its own work in **kata**. Run `kata quickstart` at the
start of each session for the agent contract; the short version:

- Author defaults to `$KATA_AUTHOR > $USER > git user.name`; set
  `KATA_AUTHOR` only if you need a different actor (e.g. an agent
  handle distinct from your login).
- `kata list --json` to see open work; `kata show <N> --json` for detail.
- Search before creating: `kata search "<keywords>" --json`.
- Update existing issues over creating duplicates (`kata comment`,
  `kata label add`, `kata block`, `kata parent`).
- Close only when the work is actually complete: `kata close <N> --reason done`.
- Never `kata delete` or `kata purge` without explicit user authorization.

For long-running work, `kata events --tail` streams NDJSON.

## Specs and plans

- Design specs: `docs/superpowers/specs/`
- Implementation plans: `docs/superpowers/plans/`

The master spec is `docs/superpowers/specs/2026-04-29-kata-design.md`.
The shared-server-mode guardrails (still relevant for the future auth
work) live in `docs/superpowers/specs/2026-04-29-kata-shared-server-mode.md`.

## Remote-client mode (no auth)

A daemon can serve clients on other hosts over a private network:

- Server: `kata daemon start --listen 100.64.0.5:7777`, or set
  `listen = "100.64.0.5:7777"` in `<KATA_HOME>/config.toml` so every
  daemon (including the auto-started one) binds TCP.
- Client: `export KATA_SERVER=http://100.64.0.5:7777` or commit a
  gitignored `.kata.local.toml` with `[server] url = "..."` next to
  `.kata.toml`. `KATA_SERVER` env wins.
- Init and resolution are both path-free whenever the client can
  derive identity locally (existing `.kata.toml`, `--project`, or a
  git workspace): the client sends `project_identity` and writes
  `.kata.toml` itself; the daemon never stats the client's filesystem.
  `kata init` falls back to a path-based request only when none of
  those sources are available, so the daemon (or its absence) emits
  the existing validation error. No auth yet — network ACLs are the
  boundary.

See `docs/superpowers/specs/2026-05-04-kata-remote-client-design.md`.
