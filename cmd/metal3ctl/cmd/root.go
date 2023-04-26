package cmd

import (
	"fmt"
	"os"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

type stackTracer interface {
	StackTrace() errors.StackTrace
}

var (
	cfgFile   string
	verbosity *int
)

// RootCmd is metal3ctl root CLI command.
var RootCmd = &cobra.Command{
	Use:          "metal3ctl",
	SilenceUsage: true,
	Short:        "metal3ctl controls BMO and ironic in the management cluster",
	Long:         "Get started with metal3 using metal3ctl to install and manage BMO and Ironic in your metal3 management cluster.",
}

func Execute() {
	if err := RootCmd.Execute(); err != nil {
		if verbosity != nil && *verbosity >= 5 {
			if err, ok := err.(stackTracer); ok {
				for _, f := range err.StackTrace() {
					fmt.Fprintf(os.Stderr, "%+s:%d\n", f, f)
				}
			}
		}
		// TODO: print cmd help if validation error
		os.Exit(1)
	}
}
