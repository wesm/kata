package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/wesm/kata/internal/config"
	"github.com/wesm/kata/internal/daemon"
	"github.com/wesm/kata/internal/daemonclient"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/jsonl"
)

func newExportCmd() *cobra.Command {
	var output string
	var projectID int64
	var includeDeleted bool
	var allowRunningDaemon bool
	cmd := &cobra.Command{
		Use:   "export",
		Short: "export the kata database as JSONL",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if !allowRunningDaemon {
				if err := refuseRunningDaemon(ctx); err != nil {
					return err
				}
			}
			dbPath, err := config.KataDB()
			if err != nil {
				return err
			}
			if output == "" {
				output = "kata-export-" + time.Now().UTC().Format("20060102T150405Z") + ".jsonl"
			}
			d, err := db.Open(ctx, dbPath)
			if err != nil {
				return err
			}
			defer func() { _ = d.Close() }()
			f, err := os.OpenFile(output, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600) //nolint:gosec // output path is user-supplied via --output CLI flag
			if err != nil {
				return fmt.Errorf("open export output: %w", err)
			}
			bw := bufio.NewWriter(f)
			if err := jsonl.Export(ctx, d, bw, jsonl.ExportOptions{
				ProjectID:      projectID,
				IncludeDeleted: includeDeleted,
			}); err != nil {
				_ = f.Close()
				return err
			}
			if err := bw.Flush(); err != nil {
				_ = f.Close()
				return fmt.Errorf("flush export output: %w", err)
			}
			if err := f.Sync(); err != nil {
				_ = f.Close()
				return fmt.Errorf("sync export output: %w", err)
			}
			if err := f.Close(); err != nil {
				return fmt.Errorf("close export output: %w", err)
			}
			if !flags.Quiet && !flags.JSON {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "exported %s\n", output)
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&output, "output", "", "path to write JSONL export")
	cmd.Flags().Int64Var(&projectID, "project-id", 0, "export only one project id")
	cmd.Flags().BoolVar(&includeDeleted, "include-deleted", true, "include soft-deleted rows")
	cmd.Flags().BoolVar(&allowRunningDaemon, "allow-running-daemon", false, "export even if a daemon is running")
	return cmd
}

func refuseRunningDaemon(ctx context.Context) error {
	return refuseRunningDaemonWithMessage(ctx,
		"daemon is running for this database; stop it or pass --allow-running-daemon")
}

func refuseRunningDaemonWithMessage(ctx context.Context, message string) error {
	ns, err := daemon.NewNamespace()
	if err != nil {
		return err
	}
	if _, ok := daemonclient.Discover(ctx, ns.DataDir); ok {
		return &cliError{
			Message:  message,
			Kind:     kindValidation,
			ExitCode: ExitValidation,
		}
	}
	return nil
}
