package main

import "github.com/spf13/cobra"

// newAuditCmd returns the parent `audit` command. It is a grouping
// command — its subcommands carry the real behavior. The first
// subcommand is `audit closes`, a read-only projection of close events
// for after-the-fact review of agent activity.
func newAuditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "audit recent activity (close events, etc.)",
		Long: `kata audit groups read-only commands for reviewing recent
activity in a project. The subcommands surface event projections that
make agent behavior inspectable after the fact.

Subcommands:
  closes  list close events with filters (actor, reason, parent, ...)`,
	}
	cmd.AddCommand(newAuditClosesCmd())
	return cmd
}
