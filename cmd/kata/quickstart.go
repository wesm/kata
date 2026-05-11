package main

import (
	"bytes"
	"fmt"

	"github.com/spf13/cobra"
)

const agentQuickstartText = `# kata agent quickstart

Use kata as the shared issue ledger for this workspace.

1. Run from the project workspace, or pass --workspace <path>.
   To work on another project from any directory, pass --project <name>.
2. Author defaults to $KATA_AUTHOR > $USER > git user.name; set KATA_AUTHOR
   only when you need an actor different from your login.
3. Prefer --json for reads and writes when parsing output.
4. If the workspace is not initialized, report that kata init is needed.
5. Issue refs use short_ids derived from each issue's ULID. In a workspace
   bound to a project, the bare form is enough (e.g. abc4). Cross-project
   references qualify with the project name (e.g. kata#abc4). Full 26-char
   ULIDs also resolve. Legacy numeric refs (12, kata#12) no longer work.
   The examples below use abc4 and d4ex as placeholders for short_ids you
   will read out of "kata create --json" / "kata search --json" responses.
6. Search before creating:

   kata search "login race" --json
   kata search --project foo "login race" --json

7. If no existing issue fits, create with an idempotency key:

   kata create "fix login race" \
     --body "Observed double-submit in Safari callback." \
     --idempotency-key "login-race-2026-05-02" \
     --json

   kata create --project foo "fix login race" \
     --body "Observed double-submit in Safari callback." \
     --idempotency-key "foo-login-race-2026-05-02" \
     --json

8. Prefer updating existing issues over creating duplicates:

   kata show abc4 --json
   kata show --project foo abc4 --json
   kata comment abc4 --body "Found another reproduction path." --json
   kata label add abc4 safari --json
   kata edit abc4 --blocks d4ex --json

9. Use relationships deliberately. They live as flags on create + edit and
   are framed from the operating issue's POV — no argument-order traps:

   parent      = this issue is a sub-task of a larger issue
   blocks      = this issue must be resolved before the target can proceed
   blocked_by  = the target must be resolved before this issue can proceed
   related     = useful context, but not ordering

   kata create "fix auth flow" --parent abc4 --blocked-by d4ex --related j7m2 --json
   kata edit abc4 --remove-blocks d4ex --related j7m2 --json

   --remove-parent <ref> is strict: it must equal the current parent or
   fail loudly. Read parent before asserting a removal. The other
   --remove-* flags are idempotent (no-op when the link is already gone).

10. Close only when the work is actually complete:

    kata close abc4 --reason done --json

11. Do not run delete or purge unless the user explicitly asks for that exact
    destructive action and issue ref.

For long-running agents, poll events:

   kata events --after 0 --limit 100 --json

Remember the returned cursor and resume from it. If a response says
reset_required, discard cached kata state and resume from the reset cursor.

For live streams:

   kata events --tail

The tail stream emits newline-delimited JSON.

# Remote daemon (optional)

When the kata daemon runs on a different host, point clients at it with
KATA_SERVER:

   export KATA_SERVER=http://100.64.0.5:7777

Or commit-free per-workspace:

   # .kata.local.toml (gitignored by 'kata init')
   version = 1

   [server]
   url = "http://100.64.0.5:7777"

KATA_SERVER wins over the file when both are set. A configured-but-down
remote returns exit 7 (kata server not responding) — no silent fallback to
spawning a local daemon. The default (no env, no .kata.local.toml) is
unchanged: a local Unix-socket daemon is auto-started on demand.
`

func newQuickstartCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "quickstart",
		Aliases: []string{"agent-instructions"},
		Short:   "print instructions for agents using kata",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if flags.JSON {
				var buf bytes.Buffer
				if err := emitJSON(&buf, map[string]string{
					"quickstart": agentQuickstartText,
				}); err != nil {
					return err
				}
				_, err := fmt.Fprint(cmd.OutOrStdout(), buf.String())
				return err
			}
			_, err := fmt.Fprint(cmd.OutOrStdout(), agentQuickstartText)
			return err
		},
	}
}
