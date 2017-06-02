package dockerfile

// This file contains the dispatchers for each command. Note that
// `nullDispatch` is not actually a command, but support for commands we parse
// but do nothing with.
//
// See evaluator.go for a higher level discussion of the whole evaluator
// package.

import (
	"bytes"
	"fmt"
	"runtime"
	"sort"
	"strings"

	"github.com/Sirupsen/logrus"
	"github.com/docker/docker/api"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/strslice"
	"github.com/docker/docker/builder"
	"github.com/docker/docker/builder/dockerfile/instructions"
	"github.com/docker/docker/builder/dockerfile/parser"
	"github.com/docker/docker/pkg/signal"
	"github.com/docker/go-connections/nat"
	"github.com/pkg/errors"
)

func copyEnv(previous []string) []string {
	result := make([]string, len(previous))
	copy(result, previous)
	return result
}

type baseCommand struct {
	builder         *Builder
	dispatchMessage string
}

func (c *baseCommand) writeDispatchMessage() {
	fmt.Fprintln(c.builder.Stdout, c.dispatchMessage)
}

// ENV foo bar
//
// Sets the environment variable foo to bar, also makes interpolation
// in the dockerfile available from the next statement on via ${foo}.
//
func (d *stageBuilder) dispatchEnv(c *instructions.EnvCommand) error {
	runConfig := d.state.runConfig
	commitMessage := bytes.NewBufferString("ENV")
	for _, e := range c.Env {
		name := e.Key
		newVar := e.String()

		commitMessage.WriteString(" " + newVar)
		gotOne := false
		for i, envVar := range runConfig.Env {
			envParts := strings.SplitN(envVar, "=", 2)
			compareFrom := envParts[0]
			if equalEnvKeys(compareFrom, name) {
				runConfig.Env[i] = newVar
				gotOne = true
				break
			}
		}
		if !gotOne {
			runConfig.Env = append(runConfig.Env, newVar)
		}
	}
	return d.builder.commit(d.state, commitMessage.String())
}

// MAINTAINER some text <maybe@an.email.address>
//
// Sets the maintainer metadata.
func (d *stageBuilder) dispatchMaintainer(c *instructions.MaintainerCommand) error {

	d.state.maintainer = c.Maintainer
	return d.builder.commit(d.state, "MAINTAINER "+c.Maintainer)
}

// LABEL some json data describing the image
//
// Sets the Label variable foo to bar,
//
func (d *stageBuilder) dispatchLabel(c *instructions.LabelCommand) error {
	if d.state.runConfig.Labels == nil {
		d.state.runConfig.Labels = make(map[string]string)
	}
	commitStr := "LABEL"
	for _, v := range c.Labels {
		d.state.runConfig.Labels[v.Key] = v.Value
		commitStr += " " + v.String()
	}
	return d.builder.commit(d.state, commitStr)
}

// ADD foo /path
//
// Add the file 'foo' to '/path'. Tarball and Remote URL (git, http) handling
// exist here. If you do not wish to have this automatic handling, use COPY.
//
func (d *stageBuilder) dispatchAdd(c *instructions.AddCommand) error {
	downloader := newRemoteSourceDownloader(d.builder.Output, d.builder.Stdout)
	copier := newCopier(d.source, d.builder.pathCache, downloader, nil)
	defer copier.Cleanup()

	copyInstruction, err := copier.createCopyInstruction(append(c.Srcs, c.Dest), "ADD")
	if err != nil {
		return err
	}
	copyInstruction.allowLocalDecompression = true

	return d.builder.performCopy(d.state, copyInstruction)
}

// COPY foo /path
//
// Same as 'ADD' but without the tar and remote url handling.
//
func (d *stageBuilder) dispatchCopy(c *instructions.CopyCommand) error {
	var im *imageMount
	var err error
	if c.From != "" {
		im, err = d.builder.getImageMount(c.From)
		if err != nil {
			return err
		}
	}
	copier := newCopier(d.source, d.builder.pathCache, errOnSourceDownload, im)
	defer copier.Cleanup()
	copyInstruction, err := copier.createCopyInstruction(append(c.Srcs, c.Dest), "COPY")
	if err != nil {
		return err
	}

	return d.builder.performCopy(d.state, copyInstruction)
}

func (b *Builder) getImageMount(imageRefOrID string) (*imageMount, error) {
	if imageRefOrID == "" {
		// TODO: this could return the source in the default case as well?
		return nil, nil
	}

	stage, err := b.buildStages.get(imageRefOrID)
	if err != nil {
		return nil, err
	}
	if stage != nil {
		imageRefOrID = stage.ImageID()
	}
	return b.imageSources.Get(imageRefOrID)
}

// FROM imagename[:tag | @digest] [AS build-stage-name]
//
func (d *stageBuilder) dispatchFrom(cmd *instructions.FromCommand) error {
	d.builder.resetImageCache()
	image, err := d.builder.getFromImage(d.shlex, cmd.BaseName)
	if err != nil {
		return err
	}
	if err := d.builder.buildStages.add(cmd.StageName, image); err != nil {
		return err
	}
	state := d.state
	state.beginStage(cmd.StageName, image)

	if state.runConfig.OnBuild != nil {
		triggers := state.runConfig.OnBuild
		state.runConfig.OnBuild = nil
		for _, trigger := range triggers {
			ast, err := parser.Parse(strings.NewReader(trigger))
			if err != nil {
				return err
			}
			if len(ast.AST.Children) != 1 {
				return errors.New("onbuild trigger should be a single expression")
			}
			cmd, err := instructions.ParseCommand(ast.AST.Children[0])
			if err != nil {
				if strings.Index(err.Error(), "unknown instruction:") == 0 {
					buildsFailed.WithValues(metricsUnknownInstructionError).Inc()
				}
				return err
			}
			err = d.dispatch(cmd)
			if err != nil {
				return err
			}
		}
	}
	state.hasDispatchedFrom = true
	return nil
}

// scratchImage is used as a token for the empty base image. It uses buildStage
// as a convenient implementation of builder.Image, but is not actually a
// buildStage.
var scratchImage builder.Image = &buildStage{}

func (b *Builder) getExpandedImageName(shlex *ShellLex, name string) (string, error) {
	substitutionArgs := []string{}
	for key, value := range b.buildArgs.GetAllMeta() {
		substitutionArgs = append(substitutionArgs, key+"="+value)
	}

	name, err := shlex.ProcessWord(name, substitutionArgs)
	if err != nil {
		return "", err
	}
	return name, nil
}
func (b *Builder) getImageOrStage(name string) (builder.Image, error) {
	if im, ok := b.buildStages.getByName(name); ok {
		return im, nil
	}

	// Windows cannot support a container with no base image.
	if name == api.NoBaseImageSpecifier {
		if runtime.GOOS == "windows" {
			return nil, errors.New("Windows does not support FROM scratch")
		}
		return scratchImage, nil
	}
	imageMount, err := b.imageSources.Get(name)
	if err != nil {
		return nil, err
	}
	return imageMount.Image(), nil
}
func (b *Builder) getFromImage(shlex *ShellLex, name string) (builder.Image, error) {
	name, err := b.getExpandedImageName(shlex, name)
	if err != nil {
		return nil, err
	}
	return b.getImageOrStage(name)
}

// TODO: At dispatch time ? Drop ONBUILD support

// func processOnBuild(req parseRequest) error {
// 	image, err := req.builder.getFromImage(req.shlex, req.args[0])
// 	if err != nil {
// 		return err
// 	}
// 	onBuilds := image.RunConfig().OnBuild
// 	// Process ONBUILD triggers if they exist
// 	// parse the ONBUILD triggers by invoking the parser
// 	for _, step := range onBuilds {
// 		dockerfile, err := parser.Parse(strings.NewReader(step))
// 		if err != nil {
// 			return err
// 		}

// 		for _, n := range dockerfile.AST.Children {
// 			if err := checkDispatch(n); err != nil {
// 				return err
// 			}

// 			upperCasedCmd := strings.ToUpper(n.Value)
// 			switch upperCasedCmd {
// 			case "ONBUILD":
// 				return errors.New("Chaining ONBUILD via `ONBUILD ONBUILD` isn't allowed")
// 			case "MAINTAINER", "FROM":
// 				return errors.Errorf("%s isn't allowed as an ONBUILD trigger", upperCasedCmd)
// 			}
// 		}

// 		if _, err := parseFromDockerfile(req.builder, dockerfile, req.state); err != nil {
// 			return err
// 		}
// 	}
// 	return nil
// }
// ONBUILD RUN echo yo
//
// ONBUILD triggers run when the image is used in a FROM statement.
//
// ONBUILD handling has a lot of special-case functionality, the heading in
// evaluator.go and comments around dispatch() in the same file explain the
// special cases. search for 'OnBuild' in internals.go for additional special
// cases.
//
func (d *stageBuilder) dispatchOnbuild(c *instructions.OnbuildCommand) error {

	d.state.runConfig.OnBuild = append(d.state.runConfig.OnBuild, c.Expression)
	return d.builder.commit(d.state, "ONBUILD "+c.Expression)
}

// WORKDIR /tmp
//
// Set the working directory for future RUN/CMD/etc statements.
//
func (d *stageBuilder) dispatchWorkdir(c *instructions.WorkdirCommand) error {
	runConfig := d.state.runConfig
	var err error
	runConfig.WorkingDir, err = normaliseWorkdir(runConfig.WorkingDir, c.Path)
	if err != nil {
		return err
	}

	// For performance reasons, we explicitly do a create/mkdir now
	// This avoids having an unnecessary expensive mount/unmount calls
	// (on Windows in particular) during each container create.
	// Prior to 1.13, the mkdir was deferred and not executed at this step.
	if d.builder.disableCommit {
		// Don't call back into the daemon if we're going through docker commit --change "WORKDIR /foo".
		// We've already updated the runConfig and that's enough.
		return nil
	}

	comment := "WORKDIR " + runConfig.WorkingDir
	runConfigWithCommentCmd := copyRunConfig(runConfig, withCmdCommentString(comment))
	if hit, err := d.builder.probeCache(d.state, runConfigWithCommentCmd); err != nil || hit {
		return err
	}

	container, err := d.builder.docker.ContainerCreate(types.ContainerCreateConfig{
		Config: runConfigWithCommentCmd,
		// Set a log config to override any default value set on the daemon
		HostConfig: &container.HostConfig{LogConfig: defaultLogConfig},
	})
	if err != nil {
		return err
	}
	d.builder.tmpContainers[container.ID] = struct{}{}
	if err := d.builder.docker.ContainerCreateWorkdir(container.ID); err != nil {
		return err
	}

	return d.builder.commitContainer(d.state, container.ID, runConfigWithCommentCmd)
}

// RUN some command yo
//
// run a command and commit the image. Args are automatically prepended with
// the current SHELL which defaults to 'sh -c' under linux or 'cmd /S /C' under
// Windows, in the event there is only one argument The difference in processing:
//
// RUN echo hi          # sh -c echo hi       (Linux)
// RUN echo hi          # cmd /S /C echo hi   (Windows)
// RUN [ "echo", "hi" ] # echo hi
//
func (d *stageBuilder) dispatchRun(c *instructions.RunCommand) error {

	stateRunConfig := d.state.runConfig
	cmdFromArgs := c.Expression
	if c.PrependShell {
		cmdFromArgs = append(getShell(stateRunConfig), cmdFromArgs...)
	}
	buildArgs := d.builder.buildArgs.FilterAllowed(stateRunConfig.Env)

	saveCmd := cmdFromArgs
	if len(buildArgs) > 0 {
		saveCmd = prependEnvOnCmd(d.builder.buildArgs, buildArgs, cmdFromArgs)
	}

	runConfigForCacheProbe := copyRunConfig(stateRunConfig,
		withCmd(saveCmd),
		withEntrypointOverride(saveCmd, nil))
	hit, err := d.builder.probeCache(d.state, runConfigForCacheProbe)
	if err != nil || hit {
		return err
	}

	runConfig := copyRunConfig(stateRunConfig,
		withCmd(cmdFromArgs),
		withEnv(append(stateRunConfig.Env, buildArgs...)),
		withEntrypointOverride(saveCmd, strslice.StrSlice{""}))

	// set config as already being escaped, this prevents double escaping on windows
	runConfig.ArgsEscaped = true

	logrus.Debugf("[BUILDER] Command to be executed: %v", runConfig.Cmd)
	cID, err := d.builder.create(runConfig)
	if err != nil {
		return err
	}
	if err := d.builder.run(cID, runConfig.Cmd); err != nil {
		return err
	}

	return d.builder.commitContainer(d.state, cID, runConfigForCacheProbe)
}

// Derive the command to use for probeCache() and to commit in this container.
// Note that we only do this if there are any build-time env vars.  Also, we
// use the special argument "|#" at the start of the args array. This will
// avoid conflicts with any RUN command since commands can not
// start with | (vertical bar). The "#" (number of build envs) is there to
// help ensure proper cache matches. We don't want a RUN command
// that starts with "foo=abc" to be considered part of a build-time env var.
//
// remove any unreferenced built-in args from the environment variables.
// These args are transparent so resulting image should be the same regardless
// of the value.
func prependEnvOnCmd(buildArgs *buildArgs, buildArgVars []string, cmd strslice.StrSlice) strslice.StrSlice {
	var tmpBuildEnv []string
	for _, env := range buildArgVars {
		key := strings.SplitN(env, "=", 2)[0]
		if buildArgs.IsReferencedOrNotBuiltin(key) {
			tmpBuildEnv = append(tmpBuildEnv, env)
		}
	}

	sort.Strings(tmpBuildEnv)
	tmpEnv := append([]string{fmt.Sprintf("|%d", len(tmpBuildEnv))}, tmpBuildEnv...)
	return strslice.StrSlice(append(tmpEnv, cmd...))
}

// CMD foo
//
// Set the default command to run in the container (which may be empty).
// Argument handling is the same as RUN.
//
func (d *stageBuilder) dispatchCmd(c *instructions.CmdCommand) error {
	runConfig := d.state.runConfig
	cmd := c.Cmd
	if c.PrependShell {
		cmd = append(getShell(runConfig), cmd...)
	}
	runConfig.Cmd = cmd
	// set config as already being escaped, this prevents double escaping on windows
	runConfig.ArgsEscaped = true

	if err := d.builder.commit(d.state, fmt.Sprintf("CMD %q", cmd)); err != nil {
		return err
	}

	if len(c.Cmd) != 0 {
		d.state.cmdSet = true
	}

	return nil
}

// HEALTHCHECK foo
//
// Set the default healthcheck command to run in the container (which may be empty).
// Argument handling is the same as RUN.
//
func (d *stageBuilder) dispatchHealthcheck(c *instructions.HealthCheckCommand) error {
	runConfig := d.state.runConfig
	if runConfig.Healthcheck != nil {
		oldCmd := runConfig.Healthcheck.Test
		if len(oldCmd) > 0 && oldCmd[0] != "NONE" {
			fmt.Fprintf(d.builder.Stdout, "Note: overriding previous HEALTHCHECK: %v\n", oldCmd)
		}
	}
	runConfig.Healthcheck = c.Health
	return d.builder.commit(d.state, fmt.Sprintf("HEALTHCHECK %q", runConfig.Healthcheck))
}

// ENTRYPOINT /usr/sbin/nginx
//
// Set the entrypoint to /usr/sbin/nginx. Will accept the CMD as the arguments
// to /usr/sbin/nginx. Uses the default shell if not in JSON format.
//
// Handles command processing similar to CMD and RUN, only req.runConfig.Entrypoint
// is initialized at newBuilder time instead of through argument parsing.
//
func (d *stageBuilder) dispatchEntrypoint(c *instructions.EntrypointCommand) error {
	runConfig := d.state.runConfig
	cmd := c.Cmd
	if cmd != nil {
		if c.PrependShell {
			cmd = append(getShell(runConfig), cmd...)
		}
	}
	runConfig.Entrypoint = cmd
	if !d.state.cmdSet {
		runConfig.Cmd = nil
	}

	return d.builder.commit(d.state, fmt.Sprintf("ENTRYPOINT %q", runConfig.Entrypoint))
}

// EXPOSE 6667/tcp 7000/tcp
//
// Expose ports for links and port mappings. This all ends up in
// req.runConfig.ExposedPorts for runconfig.
//
func (d *stageBuilder) dispatchExpose(c *instructions.ExposeCommand, envs []string) error {
	// custom multi word expansion
	ports := []string{}
	for _, p := range c.Ports {
		ps, err := d.shlex.ProcessWords(p, envs)
		if err != nil {
			return err
		}
		ports = append(ports, ps...)
	}
	c.Ports = ports

	if d.state.runConfig.ExposedPorts == nil {
		d.state.runConfig.ExposedPorts = make(nat.PortSet)
	}
	for _, p := range c.Ports {
		d.state.runConfig.ExposedPorts[nat.Port(p)] = struct{}{}
	}

	return d.builder.commit(d.state, "EXPOSE "+strings.Join(c.Ports, " "))
}

// USER foo
//
// Set the user to 'foo' for future commands and when running the
// ENTRYPOINT/CMD at container run time.
//
func (d *stageBuilder) dispatchUser(c *instructions.UserCommand) error {
	d.state.runConfig.User = c.User
	return d.builder.commit(d.state, fmt.Sprintf("USER %v", c.User))
}

// VOLUME /foo
//
// Expose the volume /foo for use. Will also accept the JSON array form.
//
func (d *stageBuilder) dispatchVolume(c *instructions.VolumeCommand) error {
	if d.state.runConfig.Volumes == nil {
		d.state.runConfig.Volumes = map[string]struct{}{}
	}
	for _, v := range c.Volumes {
		d.state.runConfig.Volumes[v] = struct{}{}
	}
	return d.builder.commit(d.state, fmt.Sprintf("VOLUME %v", c.Volumes))
}

// STOPSIGNAL signal
//
// Set the signal that will be used to kill the container.
func (d *stageBuilder) dispatchStopSignal(c *instructions.StopSignalCommand) error {

	_, err := signal.ParseSignal(c.Signal)
	if err != nil {
		return err
	}
	d.state.runConfig.StopSignal = c.Signal
	return d.builder.commit(d.state, fmt.Sprintf("STOPSIGNAL %v", c.Signal))
}

// ARG name[=value]
//
// Adds the variable foo to the trusted list of variables that can be passed
// to builder using the --build-arg flag for expansion/substitution or passing to 'run'.
// Dockerfile author may optionally set a default value of this variable.
func (d *stageBuilder) dispatchArg(c *instructions.ArgCommand) error {

	commitStr := "ARG " + c.Name
	if c.Value != nil {
		commitStr += "=" + *c.Value
	}

	d.builder.buildArgs.AddArg(c.Name, c.Value)
	return d.builder.commit(d.state, commitStr)
}

// SHELL powershell -command
//
// Set the non-default shell to use.
func (d *stageBuilder) dispatchShell(c *instructions.ShellCommand) error {
	d.state.runConfig.Shell = c.Shell
	return d.builder.commit(d.state, fmt.Sprintf("SHELL %v", d.state.runConfig.Shell))
}

func (d *stageBuilder) dispatchResumeBuild(c *instructions.ResumeBuildCommand) error {
	d.state.runConfig = c.BaseConfig
	d.state.imageID = c.BaseConfig.Image
	return nil
}
