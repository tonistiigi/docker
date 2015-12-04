package client

import (
	"errors"
	"fmt"
	"net/url"

	Cli "github.com/docker/docker/cli"
	flag "github.com/docker/docker/pkg/mflag"
	"github.com/docker/docker/reference"
	"github.com/docker/docker/registry"
)

var errTagCantBeUsed = errors.New("tag can't be used with --all-tags/-a")

// CmdPull pulls an image or a repository from the registry.
//
// Usage: docker pull [OPTIONS] IMAGENAME[:TAG|@DIGEST]
func (cli *DockerCli) CmdPull(args ...string) error {
	cmd := Cli.Subcmd("pull", []string{"NAME[:TAG|@DIGEST]"}, Cli.DockerCommands["pull"].Description, true)
	allTags := cmd.Bool([]string{"a", "-all-tags"}, false, "Download all tagged images in the repository")
	addTrustedFlags(cmd, true)
	cmd.Require(flag.Exact, 1)

	cmd.ParseFlags(args, true)
	remote := cmd.Arg(0)

	distributionRef, err := reference.ParseNamed(remote)
	if err != nil {
		return err
	}

	var tag string
	switch x := distributionRef.(type) {
	case reference.Canonical:
		if *allTags {
			return errTagCantBeUsed
		}
		tag = x.Digest().String()
	case reference.NamedTagged:
		if *allTags {
			return errTagCantBeUsed
		}
		tag = x.Tag()
	default:
		if !*allTags {
			tag = reference.DefaultTag
			distributionRef, err = reference.WithTag(distributionRef, tag)
			if err != nil {
				return err
			}
			fmt.Fprintf(cli.out, "Using default tag: %s\n", tag)
		}
	}

	ref := registry.ParseReference(tag)

	// Resolve the Repository name from fqn to RepositoryInfo
	repoInfo, err := registry.ParseRepositoryInfo(distributionRef)
	if err != nil {
		return err
	}

	if isTrusted() && !ref.HasDigest() {
		// Check if tag is digest
		authConfig := registry.ResolveAuthConfig(cli.configFile, repoInfo.Index)
		return cli.trustedPull(repoInfo, ref, authConfig)
	}

	v := url.Values{}
	v.Set("fromImage", distributionRef.String())

	_, _, err = cli.clientRequestAttemptLogin("POST", "/images/create?"+v.Encode(), nil, cli.out, repoInfo.Index, "pull")
	return err
}
