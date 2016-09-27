// +build experimental

package stack

import (
	"fmt"

	"github.com/spf13/cobra"
	"golang.org/x/net/context"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/cli"
	"github.com/docker/docker/cli/command"
)

const (
	defaultNetworkDriver = "overlay"
)

type deployOptions struct {
	bundle       string
	name         string
	registryAuth bool
}

func newDeployCommand(dockerCli *command.DockerCli) *cobra.Command {
	var opts deployOptions

	cmd := &cobra.Command{
		Use:     "deploy [OPTIONS] BUNDLE",
		Aliases: []string{"up"},
		Short:   "Create and update a stack from a bundle",
		Args:    cli.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.bundle = args[0]
			return runDeploy(dockerCli, opts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.name, "name", "", "Stack name")
	flags.BoolVar(&opts.registryAuth, "with-registry-auth", false, "Send registry authentication details to swarm agents")
	return cmd
}

func runDeploy(dockerCli *command.DockerCli, opts deployOptions) error {
	client := dockerCli.Client()
	ctx := context.Background()

	createOpts := types.StackCreateOptions{
		Bundle: opts.bundle,
		Name:   opts.name,
	}

	// only send auth if flag was set
	if opts.registryAuth {
		// Retrieve encoded auth token from the image reference
		encodedAuth, err := command.RetrieveAuthTokenFromImage(ctx, dockerCli, opts.bundle)
		if err != nil {
			return err
		}
		createOpts.EncodedRegistryAuth = encodedAuth
	}

	response, err := client.StackCreate(ctx, createOpts)
	if err != nil {
		return err
	}

	for _, serviceID := range response.ServiceIDs {
		fmt.Fprintf(dockerCli.Out(), "%s\n", serviceID)
	}
	return nil
}
