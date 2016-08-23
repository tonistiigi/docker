package bundle

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/net/context"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/cli"
	"github.com/docker/docker/cli/command"
	"github.com/docker/docker/cli/command/image"
	"github.com/docker/docker/pkg/jsonmessage"
	"github.com/docker/docker/reference"
	"github.com/docker/docker/registry"
	"github.com/spf13/cobra"
)

type pullOptions struct {
	remote string
}

// newPullCommand creates a new `docker bundle pull` command
func newPullCommand(dockerCli *command.DockerCli) *cobra.Command {
	var opts pullOptions

	cmd := &cobra.Command{
		Use:   "pull [OPTIONS] NAME[:TAG|@DIGEST]",
		Short: "Pull a bundle from a registry",
		Args:  cli.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.remote = args[0]
			return runPull(dockerCli, opts)
		},
	}

	flags := cmd.Flags()
	command.AddTrustedFlags(flags, true)
	return cmd
}

func runPull(dockerCli *command.DockerCli, opts pullOptions) error {
	distributionRef, err := reference.ParseNamed(opts.remote)
	if err != nil {
		return err
	}

	if reference.IsNameOnly(distributionRef) {
		distributionRef = reference.WithDefaultTag(distributionRef)
		fmt.Fprintf(dockerCli.Out(), "Using default tag: %s\n", reference.DefaultTag)
	}

	var tag string
	switch x := distributionRef.(type) {
	case reference.Canonical:
		tag = x.Digest().String()
	case reference.NamedTagged:
		tag = x.Tag()
	}

	registryRef := registry.ParseReference(tag)

	// Resolve the Repository name from fqn to RepositoryInfo
	repoInfo, err := registry.ParseRepositoryInfo(distributionRef)
	if err != nil {
		return err
	}

	ctx := context.Background()

	authConfig := command.ResolveAuthConfig(ctx, dockerCli, repoInfo.Index)
	requestPrivilege := command.RegistryAuthenticationPrivilegedFunc(dockerCli, repoInfo.Index, "pull")

	if command.IsTrusted() && !registryRef.HasDigest() {
		// Check if tag is digest
		err = image.TrustedPull(ctx, dockerCli, repoInfo, registryRef, authConfig, requestPrivilege)
	} else {
		err = bundlePullPrivileged(ctx, dockerCli, authConfig, distributionRef.String(), requestPrivilege)
	}
	if err != nil {
		if strings.Contains(err.Error(), "target is a plugin") {
			return errors.New(err.Error() + " - Use `docker plugin install`")
		}
		return err
	}

	return nil
}

func bundlePullPrivileged(ctx context.Context, cli *command.DockerCli, authConfig types.AuthConfig, ref string, requestPrivilege types.RequestPrivilegeFunc) error {

	encodedAuth, err := command.EncodeAuthToBase64(authConfig)
	if err != nil {
		return err
	}
	options := types.BundlePullOptions{
		RegistryAuth:  encodedAuth,
		PrivilegeFunc: requestPrivilege,
	}

	responseBody, err := cli.Client().BundlePull(ctx, ref, options)
	if err != nil {
		return err
	}
	defer responseBody.Close()

	// TODO: use new function here
	return jsonmessage.DisplayJSONMessagesToStream(responseBody, cli.Out(), nil)
}
