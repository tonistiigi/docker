package client

import (
	"fmt"
	"text/tabwriter"

	Cli "github.com/docker/docker/cli"
	flag "github.com/docker/docker/pkg/mflag"
	"github.com/docker/docker/reference"
)

// CmdPlugin is the parent subcommand for all plugin commands
// Usage: docker plugin <COMMAND> <OPTS>
func (cli *DockerCli) CmdPlugin(args ...string) error {
	description := Cli.DockerCommands["plugin"].Description + "\n\nCommands:\n"
	commands := [][]string{
		{"load", "Load a plugin"},
		{"activate", "Activate plugin"},
		{"disable", "Disable"},
		{"rm", "Remove plugin"},
		{"set", "Set plugin opt"},
		{"ls", "List plugins"},
	}

	for _, cmd := range commands {
		description += fmt.Sprintf("  %-25.25s%s\n", cmd[0], cmd[1])
	}

	description += "\nRun 'docker plugin COMMAND --help' for more information on a command"
	cmd := Cli.Subcmd("plugin", []string{"[COMMAND]"}, description, false)

	cmd.Require(flag.Exact, 0)
	err := cmd.ParseFlags(args, true)
	cmd.Usage()
	return err
}

// CmdPluginLs outputs a list of Docker plugins.
//
// Usage: docker plugin ls [OPTIONS]
func (cli *DockerCli) CmdPluginLs(args ...string) error {
	cmd := Cli.Subcmd("plugin ls", nil, "List plugins", true)

	cmd.Require(flag.Exact, 0)
	cmd.ParseFlags(args, true)

	plugins, err := cli.client.PluginList()
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(cli.out, 20, 1, 3, ' ', 0)
	fmt.Fprintf(w, "NAME \tVERSION \tACTIVE")
	fmt.Fprintf(w, "\n")

	for _, p := range plugins {
		fmt.Fprintf(w, "%s\t%s\t%v\n", p.Name, p.Version, p.Active)
	}
	w.Flush()
	return nil
}

// CmdPluginRm removes one or more plugins.
//
// Usage: docker plugin rm NAME [NAME...]
func (cli *DockerCli) CmdPluginRm(args ...string) error {
	cmd := Cli.Subcmd("plugin rm", []string{"NAME [NAME...]"}, "Remove a plugin", true)
	cmd.Require(flag.Min, 1)
	cmd.ParseFlags(args, true)

	var status = 0

	for _, name := range cmd.Args() {
		if err := cli.client.PluginRemove(name); err != nil {
			fmt.Fprintf(cli.err, "%s\n", err)
			status = 1
			continue
		}
		fmt.Fprintf(cli.out, "%s\n", name)
	}

	if status != 0 {
		return Cli.StatusError{StatusCode: status}
	}
	return nil
}

func (cli *DockerCli) CmdPluginActivate(args ...string) error {
	cmd := Cli.Subcmd("plugin activate", []string{"NAME"}, "Activate a plugin", true)
	cmd.Require(flag.Exact, 1)
	cmd.ParseFlags(args, true)

	var status = 0

	if err := cli.client.PluginActivate(cmd.Args()[0]); err != nil {
		fmt.Fprintf(cli.err, "%s\n", err)
		status = 1
	}

	if status != 0 {
		return Cli.StatusError{StatusCode: status}
	}
	return nil
}

func (cli *DockerCli) CmdPluginDisable(args ...string) error {
	cmd := Cli.Subcmd("plugin disable", []string{"NAME"}, "Disable a plugin", true)
	cmd.Require(flag.Exact, 1)
	cmd.ParseFlags(args, true)

	var status = 0

	if err := cli.client.PluginDisable(cmd.Args()[0]); err != nil {
		fmt.Fprintf(cli.err, "%s\n", err)
		status = 1
	}

	if status != 0 {
		return Cli.StatusError{StatusCode: status}
	}
	return nil
}

func (cli *DockerCli) CmdPluginLoad(args ...string) error {
	cmd := Cli.Subcmd("plugin load", []string{"NAME:VERSION"}, "load a plugin", true)
	cmd.Require(flag.Exact, 1)
	cmd.ParseFlags(args, true)

	named, err := reference.ParseNamed(cmd.Args()[0]) // FIXME: validate
	if err != nil {
		return err
	}
	named = reference.WithDefaultTag(named)
	ref, ok := named.(reference.NamedTagged)
	if !ok {
		return fmt.Errorf("invalid name: %s", named.String())
	}

	var status = 0

	if err := cli.client.PluginLoad(ref.Name(), ref.Tag()); err != nil {
		fmt.Fprintf(cli.err, "%s\n", err)
		status = 1
	}

	if status != 0 {
		return Cli.StatusError{StatusCode: status}
	}
	return nil
}
