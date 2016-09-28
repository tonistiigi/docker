// +build experimental

package bundle

import (
	"fmt"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/cli"
	"github.com/docker/docker/cli/command"
	"github.com/spf13/cobra"
	"golang.org/x/net/context"
)

type removeOptions struct {
	force   bool
	noPrune bool
}

func newRemoveCommand(dockerCli *command.DockerCli) *cobra.Command {
	var opts removeOptions

	cmd := &cobra.Command{
		Use:     "rm [OPTIONS] BUNDLE [BUNDLE...]",
		Aliases: []string{"remove"},
		Short:   "Remove one or more bundles",
		Args:    cli.RequiresMinArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRemove(dockerCli, opts, args)
		},
	}

	flags := cmd.Flags()
	flags.BoolVarP(&opts.force, "force", "f", false, "Force removal of the bundle")
	flags.BoolVar(&opts.noPrune, "no-prune", false, "Do not delete untagged parents")

	return cmd
}

func runRemove(dockerCli *command.DockerCli, opts removeOptions, bundles []string) error {
	client := dockerCli.Client()
	ctx := context.Background()

	options := types.BundleRemoveOptions{
		Force:         opts.force,
		PruneChildren: !opts.noPrune,
	}

	var errs []string
	for _, bundle := range bundles {
		dels, err := client.BundleRemove(ctx, bundle, options)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		for _, del := range dels {
			switch del.Deleted {
			case "":
				fmt.Fprintf(dockerCli.Out(), "Untagged: %s\n", del.Untagged)
			default:
				fmt.Fprintf(dockerCli.Out(), "Deleted: %s\n", del.Deleted)
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "\n"))
	}
	return nil
}
