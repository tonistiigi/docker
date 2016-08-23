// +build experimental

package bundle

import (
	"fmt"

	"github.com/docker/docker/cli"
	"github.com/docker/docker/cli/command"
	"github.com/spf13/cobra"
)

// NewBundleCommand returns a cobra command for `bundle` subcommands
func NewBundleCommand(dockerCli *command.DockerCli) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bundle",
		Short: "Manage Docker bundles",
		Args:  cli.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(dockerCli.Err(), "\n"+cmd.UsageString())
		},
	}
	cmd.AddCommand(
		newInspectCommand(dockerCli),
		newListCommand(dockerCli),
		newPullCommand(dockerCli),
		newPushCommand(dockerCli),
		newRemoveCommand(dockerCli),
		newTagCommand(dockerCli),
	)
	return cmd
}
