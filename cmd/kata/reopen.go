package main

import (
	"github.com/spf13/cobra"
)

func newReopenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reopen <issue-ref>",
		Short: "reopen a closed issue",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAction(cmd, args[0], "reopen", nil)
		},
	}
	addCommentFlag(cmd)
	return cmd
}
