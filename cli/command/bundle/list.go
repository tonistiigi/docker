// +build experimental

package bundle

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/cli"
	"github.com/docker/docker/cli/command"
	"github.com/docker/docker/opts"
	"github.com/docker/docker/pkg/stringid"
	"github.com/docker/docker/reference"
	units "github.com/docker/go-units"
	"github.com/spf13/cobra"
	"golang.org/x/net/context"
)

type listOptions struct {
	quiet  bool
	filter opts.FilterOpt
}

func newListCommand(dockerCli *command.DockerCli) *cobra.Command {
	opts := listOptions{filter: opts.NewFilterOpt()}

	cmd := &cobra.Command{
		Use:     "ls [OPTIONS]",
		Aliases: []string{"list"},
		Short:   "List bundles",
		Args:    cli.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runList(dockerCli, opts)
		},
	}

	flags := cmd.Flags()
	flags.BoolVarP(&opts.quiet, "quiet", "q", false, "Only display IDs")
	flags.VarP(&opts.filter, "filter", "f", "Filter output based on conditions provided")

	return cmd
}

func runList(dockerCli *command.DockerCli, opts listOptions) error {
	ctx := context.Background()
	client := dockerCli.Client()

	bundles, err := client.BundleList(ctx, types.BundleListOptions{
		Filter: opts.filter.Value(),
	})
	if err != nil {
		return err
	}

	out := dockerCli.Out()
	if opts.quiet {
		printQuiet(out, bundles)
	} else {
		printTable(out, bundles)
	}
	return nil
}

const listItemFmt = "%s\t%s\t%s\t%s\n"

func printTable(out io.Writer, bundles []types.Bundle) {
	writer := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)

	// Ignore flushing errors
	defer writer.Flush()

	fmt.Fprintf(writer, listItemFmt, "REPOSITORY", "TAG", "BUNDLE ID", "CREATED")
	for _, bundle := range bundles {
		for _, repoTag := range bundleTags(bundle) {
			fmt.Fprintf(
				writer,
				listItemFmt,
				repoTag.repo,
				repoTag.tag,
				stringid.TruncateID(bundle.ID),
				createdSince(bundle.Created),
			)
		}
	}
}

type repoTag struct {
	repo string
	tag  string
}

// FIXME: this doesn't quite match `image list`
func bundleTags(bundle types.Bundle) []repoTag {
	tags := []repoTag{}

	for _, refString := range bundle.RepoTags {
		ref, err := reference.ParseNamed(refString)
		if err != nil {
			continue
		}
		if namedtag, ok := ref.(reference.NamedTagged); ok {
			tags = append(tags, repoTag{ref.Name(), namedtag.Tag()})
		}
	}
	if len(tags) > 0 {
		return tags
	}

	for _, refString := range bundle.RepoDigests {
		ref, err := reference.ParseNamed(refString)
		if err != nil {
			continue
		}
		if c, ok := ref.(reference.Canonical); ok {
			tags = append(tags, repoTag{ref.Name(), c.Digest().String()})
		}
	}
	if len(tags) == 0 {
		return []repoTag{{"<none>", "<none>"}}
	}
	return tags
}

func createdSince(created int64) string {
	return units.HumanDuration(time.Now().UTC().Sub(time.Unix(created, 0)))
}

func printQuiet(out io.Writer, bundles []types.Bundle) {
	for _, bundle := range bundles {
		fmt.Fprintln(out, bundle.ID)
	}
}
