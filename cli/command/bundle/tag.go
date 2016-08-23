// +build experimental

package bundle

import (
	"golang.org/x/net/context"

	"github.com/docker/docker/cli"
	"github.com/docker/docker/cli/command"
	"github.com/spf13/cobra"
)

type tagOptions struct {
	bundle string
	repo   string
}

// newTagCommand create a new `docker bundle tag` command
func newTagCommand(dockerCli *command.DockerCli) *cobra.Command {
	var opts tagOptions

	return &cobra.Command{
		Use:   "tag BUNDLE[:TAG] BUNDLE[:TAG]",
		Short: "Tag a bundle into a repository",
		Args:  cli.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.bundle = args[0]
			opts.repo = args[1]
			return runTag(dockerCli, opts)
		},
	}
}

func runTag(dockerCli *command.DockerCli, opts tagOptions) error {
	ctx := context.Background()
	return dockerCli.Client().BundleTag(ctx, opts.bundle, opts.repo)
}
