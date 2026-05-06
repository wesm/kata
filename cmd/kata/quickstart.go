package main

import (
	"bytes"
	"fmt"

	"github.com/spf13/cobra"
)

const agentQuickstartText = `# kata agent quickstart

Use kata as the shared issue ledger for this workspace.

1. Run from the project workspace, or pass --workspace <path>.
2. Author defaults to $KATA_AUTHOR > $USER > git user.name; set KATA_AUTHOR
   only when you need an actor different from your login.
3. Prefer --json for reads and writes when parsing output.
4. If the workspace is not initialized, report that kata init is needed.
5. Search before creating:

   kata search "login race" --json

6. If no existing issue fits, create with an idempotency key:

   kata create "fix login race" \
     --body "Observed double-submit in Safari callback." \
     --idempotency-key "login-race-2026-05-02" \
     --json

7. Prefer updating existing issues over creating duplicates:

   kata show 12 --json
   kata comment 12 --body "Found another reproduction path." --json
   kata label add 12 safari --json
   kata block 12 18 --json

8. Use relationships deliberately:

   parent  = this issue is part of a larger issue
   blocks  = the first issue must be resolved before the second can proceed
   related = useful context, but not ordering

9. Close only when the work is actually complete:

   kata close 12 --reason done --json

10. Do not run delete or purge unless the user explicitly asks for that exact
    destructive action and issue number.

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
