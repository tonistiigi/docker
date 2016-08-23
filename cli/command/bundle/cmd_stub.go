// +build !experimental

package bundle

import (
	"github.com/docker/docker/cli/command"
	"github.com/spf13/cobra"
)

// NewBundleCommand returns no command
func NewBundleCommand(dockerCli *command.DockerCli) *cobra.Command {
	return &cobra.Command{}
}
