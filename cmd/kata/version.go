package main

import (
	"bytes"
	"fmt"
	"runtime"

	"github.com/wesm/kata/internal/version"

	"github.com/spf13/cobra"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "print version information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			if flags.JSON {
				var buf bytes.Buffer
				payload := map[string]string{
					"version": version.Version,
					"commit":  version.Commit,
					"built":   version.BuildDate,
					"go":      runtime.Version(),
					"os":      runtime.GOOS,
					"arch":    runtime.GOARCH,
				}
				if err := emitJSON(&buf, payload); err != nil {
					return err
				}
				_, err := fmt.Fprint(out, buf.String())
				return err
			}
			_, err := fmt.Fprintf(out,
				"kata %s\n"+
					"  commit:  %s\n"+
					"  built:   %s\n"+
					"  go:      %s\n"+
					"  os/arch: %s/%s\n",
				version.Version, version.Commit, version.BuildDate,
				runtime.Version(), runtime.GOOS, runtime.GOARCH,
			)
			return err
		},
	}
}
