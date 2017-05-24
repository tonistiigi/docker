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
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/docker/docker/api"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/strslice"
	"github.com/docker/docker/builder"
	"github.com/docker/docker/builder/dockerfile/command"
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

func dispatchEnv(state *dispatchState, c *command.EnvCommand, expand processWordFunc) error {
	runConfig := state.runConfig
	commitMessage := bytes.NewBufferString("ENV")
	for _, e := range c.Env {
		name, err := processWordSingleOutput(e.Key, expand)
		if err != nil {
			return err
		}
		value, err := processWordSingleOutput(e.Value, expand)
		if err != nil {
			return err
		}
		newVar := name + "=" + value

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
	return state.builder.commit(state, commitMessage.String())
}

// ENV foo bar
//
// Sets the environment variable foo to bar, also makes interpolation
// in the dockerfile available from the next statement on via ${foo}.
//
func parseEnv(req parseRequest) error {
	// this one has side effect on parsing. So it is evaluated early
	if len(req.args) == 0 {
		return errAtLeastOneArgument("ENV")
	}

	if len(req.args)%2 != 0 {
		// should never get here, but just in case
		return errTooManyArguments("ENV")
	}

	if err := req.flags.Parse(); err != nil {
		return err
	}
	stage, err := req.state.CurrentStage()
	if err != nil {
		return err
	}
	var envs command.KeyValuePairs
	for j := 0; j < len(req.args); j += 2 {
		if len(req.args[j]) == 0 {
			return errBlankCommandNames("ENV")
		}
		name := req.args[j]
		value := req.args[j+1]
		envs = append(envs, command.KeyValuePair{Key: name, Value: value})
	}
	stage.AddCommand(&command.EnvCommand{
		Env:            envs,
		OriginalSource: req.original,
	})

	return nil
}
func dispatchMaintainer(state *dispatchState, c *command.MaintainerCommand, expand processWordFunc) error {
	maintainer, err := processWordSingleOutput(c.Maintainer, expand)
	if err != nil {
		return err
	}
	state.maintainer = maintainer
	return state.builder.commit(state, "MAINTAINER "+maintainer)
}

// MAINTAINER some text <maybe@an.email.address>
//
// Sets the maintainer metadata.
func parseMaintainer(req parseRequest) error {
	if len(req.args) != 1 {
		return errExactlyOneArgument("MAINTAINER")
	}

	if err := req.flags.Parse(); err != nil {
		return err
	}
	stage, err := req.state.CurrentStage()
	if err != nil {
		return err
	}
	stage.AddCommand(&command.MaintainerCommand{
		Maintainer:     req.args[0],
		OriginalSource: req.original,
	})
	return nil
}

func dispatchLabel(state *dispatchState, c *command.LabelCommand, expand processWordFunc) error {
	if state.runConfig.Labels == nil {
		state.runConfig.Labels = make(map[string]string)
	}
	commitStr := "LABEL"
	for _, v := range c.Labels {
		k, err := processWordSingleOutput(v.Key, expand)
		if err != nil {
			return err
		}
		v, err := processWordSingleOutput(v.Value, expand)
		if err != nil {
			return err
		}
		state.runConfig.Labels[k] = v
		commitStr += " " + k + "=" + v
	}
	return state.builder.commit(state, commitStr)
}

// LABEL some json data describing the image
//
// Sets the Label variable foo to bar,
//
func parseLabel(req parseRequest) error {
	if len(req.args) == 0 {
		return errAtLeastOneArgument("LABEL")
	}
	if len(req.args)%2 != 0 {
		// should never get here, but just in case
		return errTooManyArguments("LABEL")
	}

	if err := req.flags.Parse(); err != nil {
		return err
	}
	stage, err := req.state.CurrentStage()
	if err != nil {
		return err
	}

	labels := command.KeyValuePairs{}

	for j := 0; j < len(req.args); j++ {
		name := req.args[j]
		if name == "" {
			return errBlankCommandNames("LABEL")
		}

		value := req.args[j+1]

		labels = append(labels, command.KeyValuePair{Key: name, Value: value})
		j++
	}
	stage.AddCommand(&command.LabelCommand{
		Labels:         labels,
		OriginalSource: req.original,
	})
	return nil
}

func dispatchAdd(state *dispatchState, c *command.AddCommand, expand processWordFunc) error {
	downloader := newRemoteSourceDownloader(state.builder.Output, state.builder.Stdout)
	copier := newCopier(state.source, state.builder.pathCache, downloader, nil)
	defer copier.Cleanup()
	srcs, err := processWordSingleOutputs(c.Srcs, expand)
	if err != nil {
		return err
	}
	dst, err := processWordSingleOutput(c.Dest, expand)
	if err != nil {
		return err
	}
	copyInstruction, err := copier.createCopyInstruction(append(srcs, dst), "ADD")
	if err != nil {
		return err
	}
	copyInstruction.allowLocalDecompression = true

	return state.builder.performCopy(state, copyInstruction)
}

// ADD foo /path
//
// Add the file 'foo' to '/path'. Tarball and Remote URL (git, http) handling
// exist here. If you do not wish to have this automatic handling, use COPY.
//
func parseAdd(req parseRequest) error {
	if len(req.args) < 2 {
		return errAtLeastTwoArguments("ADD")
	}

	if err := req.flags.Parse(); err != nil {
		return err
	}

	stage, err := req.state.CurrentStage()
	if err != nil {
		return err
	}

	stage.AddCommand(&command.AddCommand{
		Srcs:           req.args[:len(req.args)-1],
		Dest:           req.args[len(req.args)-1],
		OriginalSource: req.original,
	})
	return nil
}

func dispatchCopy(state *dispatchState, c *command.CopyCommand, expand processWordFunc) error {
	var im *imageMount
	srcs, err := processWordSingleOutputs(c.Srcs, expand)
	if err != nil {
		return err
	}
	dst, err := processWordSingleOutput(c.Dest, expand)
	if err != nil {
		return err
	}
	if c.From != "" {

		from, err := processWordSingleOutput(c.From, expand)
		if err != nil {
			return err
		}
		im, err = state.builder.getImageMount(from)
		if err != nil {
			return err
		}
	}
	copier := newCopier(state.source, state.builder.pathCache, errOnSourceDownload, im)
	defer copier.Cleanup()
	copyInstruction, err := copier.createCopyInstruction(append(srcs, dst), "COPY")
	if err != nil {
		return err
	}

	return state.builder.performCopy(state, copyInstruction)
}

// COPY foo /path
//
// Same as 'ADD' but without the tar and remote url handling.
//
func parseCopy(req parseRequest) error {
	if len(req.args) < 2 {
		return errAtLeastTwoArguments("COPY")
	}

	flFrom := req.flags.AddString("from", "")
	if err := req.flags.Parse(); err != nil {
		return err
	}

	// im, err := req.builder.getImageMount(flFrom)
	// if err != nil {
	// 	return errors.Wrapf(err, "invalid from flag value %s", flFrom.Value)
	// }

	stage, err := req.state.CurrentStage()
	if err != nil {
		return err
	}
	stage.AddCommand(&command.CopyCommand{
		Srcs:           req.args[:len(req.args)-1],
		Dest:           req.args[len(req.args)-1],
		From:           flFrom.Value,
		OriginalSource: req.original,
	})
	return nil
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

func dispatchFrom(state *dispatchState, cmd *command.FromCommand) error {
	state.builder.resetImageCache()
	image, err := state.builder.getFromImage(state.shlex, cmd.BaseName)
	if err != nil {
		return err
	}
	if err := state.builder.buildStages.add(cmd.StageName, image); err != nil {
		return err
	}
	state.beginStage(cmd.StageName, image)
	state.runConfig.OnBuild = []string{}
	state.runConfig.OpenStdin = false
	state.runConfig.StdinOnce = false
	if state.runConfig.OnBuild != nil {
		// TODO: parse & run on build
		state.runConfig.OnBuild = nil
	}

	return nil
}

// FROM imagename[:tag | @digest] [AS build-stage-name]
//
func parseFrom(req parseRequest) error {
	// HELP HERE <- do we need to validate stageName at dispatch time (do we authorize env / buildArgs in here?)
	stageName, err := parseBuildStageName(req.args)
	if err != nil {
		return err
	}
	if stageName == "" {
		stageName = strconv.Itoa(len(req.state.Stages))
	}

	if err := req.flags.Parse(); err != nil {
		return err
	}

	stage := command.BuildableStage{
		Name: stageName,
		Commands: []interface{}{&command.FromCommand{
			BaseName:       req.args[0],
			StageName:      stageName,
			OriginalSource: req.original,
		}},
	}

	req.buildArgs.ResetAllowed()
	req.state.Stages = append(req.state.Stages, stage)
	// TODO handle onbuild at dispatch time
	return nil

}

func parseBuildStageName(args []string) (string, error) {
	stageName := ""
	switch {
	case len(args) == 3 && strings.EqualFold(args[1], "as"):
		stageName = strings.ToLower(args[2])
		if ok, _ := regexp.MatchString("^[a-z][a-z0-9-_\\.]*$", stageName); !ok {
			return "", errors.Errorf("invalid name for build stage: %q, name can't start with a number or contain symbols", stageName)
		}
	case len(args) != 1:
		return "", errors.New("FROM requires either one or three arguments")
	}

	return stageName, nil
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

func dispatchOnbuild(state *dispatchState, c *command.OnbuildCommand, expand processWordFunc) error {
	ex, err := processWordSingleOutput(c.Expression, expand)
	if err != nil {
		return err
	}
	state.runConfig.OnBuild = append(state.runConfig.OnBuild, ex)
	return state.builder.commit(state, "ONBUILD "+ex)
}

// ONBUILD RUN echo yo
//
// ONBUILD triggers run when the image is used in a FROM statement.
//
// ONBUILD handling has a lot of special-case functionality, the heading in
// evaluator.go and comments around dispatch() in the same file explain the
// special cases. search for 'OnBuild' in internals.go for additional special
// cases.
//
func parseOnbuild(req parseRequest) error {
	if len(req.args) == 0 {
		return errAtLeastOneArgument("ONBUILD")
	}
	stage, err := req.state.CurrentStage()
	if err != nil {
		return err
	}
	if err := req.flags.Parse(); err != nil {
		return err
	}

	triggerInstruction := strings.ToUpper(strings.TrimSpace(req.args[0]))
	switch triggerInstruction {
	case "ONBUILD":
		return errors.New("Chaining ONBUILD via `ONBUILD ONBUILD` isn't allowed")
	case "MAINTAINER", "FROM":
		return fmt.Errorf("%s isn't allowed as an ONBUILD trigger", triggerInstruction)
	}

	original := regexp.MustCompile(`(?i)^\s*ONBUILD\s*`).ReplaceAllString(req.original, "")
	stage.AddCommand(&command.OnbuildCommand{
		Expression:     original,
		OriginalSource: req.original,
	})
	return nil

}

func dispatchWorkdir(state *dispatchState, c *command.WorkdirCommand, expand processWordFunc) error {
	runConfig := state.runConfig
	var err error
	p, err := processWordSingleOutput(c.Path, expand)
	if err != nil {
		return err
	}
	runConfig.WorkingDir, err = normaliseWorkdir(runConfig.WorkingDir, p)
	if err != nil {
		return err
	}

	// For performance reasons, we explicitly do a create/mkdir now
	// This avoids having an unnecessary expensive mount/unmount calls
	// (on Windows in particular) during each container create.
	// Prior to 1.13, the mkdir was deferred and not executed at this step.
	if state.builder.disableCommit {
		// Don't call back into the daemon if we're going through docker commit --change "WORKDIR /foo".
		// We've already updated the runConfig and that's enough.
		return nil
	}

	comment := "WORKDIR " + runConfig.WorkingDir
	runConfigWithCommentCmd := copyRunConfig(runConfig, withCmdCommentString(comment))
	if hit, err := state.builder.probeCache(state, runConfigWithCommentCmd); err != nil || hit {
		return err
	}

	container, err := state.builder.docker.ContainerCreate(types.ContainerCreateConfig{
		Config: runConfigWithCommentCmd,
		// Set a log config to override any default value set on the daemon
		HostConfig: &container.HostConfig{LogConfig: defaultLogConfig},
	})
	if err != nil {
		return err
	}
	state.builder.tmpContainers[container.ID] = struct{}{}
	if err := state.builder.docker.ContainerCreateWorkdir(container.ID); err != nil {
		return err
	}

	return state.builder.commitContainer(state, container.ID, runConfigWithCommentCmd)
}

// WORKDIR /tmp
//
// Set the working directory for future RUN/CMD/etc statements.
//
func parseWorkdir(req parseRequest) error {
	if len(req.args) != 1 {
		return errExactlyOneArgument("WORKDIR")
	}

	err := req.flags.Parse()
	if err != nil {
		return err
	}
	stage, err := req.state.CurrentStage()
	if err != nil {
		return err
	}
	stage.AddCommand(&command.WorkdirCommand{
		Path:           req.args[0],
		OriginalSource: req.original,
	})
	return nil

}

func dispatchRun(state *dispatchState, c *command.RunCommand, expand processWordFunc) error {

	stateRunConfig := state.runConfig
	cmdFromArgs, err := processWordSingleOutputs(c.Expression, expand)
	if err != nil {
		return err
	}
	if c.PrependShell {
		cmdFromArgs = append(getShell(stateRunConfig), cmdFromArgs...)
	}
	buildArgs := state.builder.buildArgs.FilterAllowed(stateRunConfig.Env)

	saveCmd := cmdFromArgs
	if len(buildArgs) > 0 {
		saveCmd = prependEnvOnCmd(state.builder.buildArgs, buildArgs, cmdFromArgs)
	}

	runConfigForCacheProbe := copyRunConfig(stateRunConfig,
		withCmd(saveCmd),
		withEntrypointOverride(saveCmd, nil))
	hit, err := state.builder.probeCache(state, runConfigForCacheProbe)
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
	cID, err := state.builder.create(runConfig)
	if err != nil {
		return err
	}
	if err := state.builder.run(cID, runConfig.Cmd); err != nil {
		return err
	}

	return state.builder.commitContainer(state, cID, runConfigForCacheProbe)
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
func parseRun(req parseRequest) error {

	if err := req.flags.Parse(); err != nil {
		return err
	}
	stage, err := req.state.CurrentStage()
	if err != nil {
		return err
	}
	prependShell := false
	args := handleJSONArgs(req.args, req.attributes)
	if !req.attributes["json"] {
		prependShell = true
	}
	cmdFromArgs := strslice.StrSlice(args)

	// todo: handle buildArgs at dispatch time
	// buildArgs := req.builder.buildArgs.FilterAllowed(req.state.env)

	// saveCmd := cmdFromArgs
	// if len(buildArgs) > 0 {
	// 	saveCmd = prependEnvOnCmd(req.builder.buildArgs, buildArgs, cmdFromArgs)
	// }

	stage.AddCommand(&command.RunCommand{
		Expression:     cmdFromArgs,
		PrependShell:   prependShell,
		OriginalSource: req.original,
	})
	return nil

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

func dispatchCmd(state *dispatchState, c *command.CmdCommand, expand processWordFunc) error {
	runConfig := state.runConfig
	cmd, err := processWordSingleOutputs(c.Cmd, expand)
	if err != nil {
		return err
	}
	if c.PrependShell {
		cmd = append(getShell(runConfig), cmd...)
	}
	runConfig.Cmd = cmd
	// set config as already being escaped, this prevents double escaping on windows
	runConfig.ArgsEscaped = true

	if err := state.builder.commit(state, fmt.Sprintf("CMD %q", cmd)); err != nil {
		return err
	}

	if len(c.Cmd) != 0 {
		state.cmdSet = true
	}

	return nil
}

// CMD foo
//
// Set the default command to run in the container (which may be empty).
// Argument handling is the same as RUN.
//
func parseCmd(req parseRequest) error {
	if err := req.flags.Parse(); err != nil {
		return err
	}
	prependShell := false
	cmdSlice := handleJSONArgs(req.args, req.attributes)
	if !req.attributes["json"] {
		prependShell = true
	}

	stage, err := req.state.CurrentStage()
	if err != nil {
		return err
	}
	stage.AddCommand(&command.CmdCommand{
		Cmd:            strslice.StrSlice(cmdSlice),
		PrependShell:   prependShell,
		OriginalSource: req.original,
	})
	return nil

}

// parseOptInterval(flag) is the duration of flag.Value, or 0 if
// empty. An error is reported if the value is given and less than minimum duration.
func parseOptInterval(f *Flag) (time.Duration, error) {
	s := f.Value
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	if d < time.Duration(container.MinimumDuration) {
		return 0, fmt.Errorf("Interval %#v cannot be less than %s", f.name, container.MinimumDuration)
	}
	return d, nil
}

func dispatchHealthcheck(state *dispatchState, c *command.HealthCheckCommand, expand processWordFunc) error {
	runConfig := state.runConfig
	if runConfig.Healthcheck != nil {
		oldCmd := runConfig.Healthcheck.Test
		if len(oldCmd) > 0 && oldCmd[0] != "NONE" {
			fmt.Fprintf(state.builder.Stdout, "Note: overriding previous HEALTHCHECK: %v\n", oldCmd)
		}
	}
	test, err := processWordSingleOutputs(c.Health.Test, expand)
	if err != nil {
		return err
	}
	c.Health.Test = test
	runConfig.Healthcheck = c.Health
	return state.builder.commit(state, fmt.Sprintf("HEALTHCHECK %q", runConfig.Healthcheck))
}

// HEALTHCHECK foo
//
// Set the default healthcheck command to run in the container (which may be empty).
// Argument handling is the same as RUN.
//
func parseHealthcheck(req parseRequest) error {
	if len(req.args) == 0 {
		return errAtLeastOneArgument("HEALTHCHECK")
	}
	stage, err := req.state.CurrentStage()
	if err != nil {
		return err
	}
	cmd := &command.HealthCheckCommand{
		OriginalSource: req.original,
	}

	typ := strings.ToUpper(req.args[0])
	args := req.args[1:]
	if typ == "NONE" {
		if len(args) != 0 {
			return errors.New("HEALTHCHECK NONE takes no arguments")
		}
		test := strslice.StrSlice{typ}
		cmd.Health = &container.HealthConfig{
			Test: test,
		}
	} else {

		healthcheck := container.HealthConfig{}

		flInterval := req.flags.AddString("interval", "")
		flTimeout := req.flags.AddString("timeout", "")
		flStartPeriod := req.flags.AddString("start-period", "")
		flRetries := req.flags.AddString("retries", "")

		if err := req.flags.Parse(); err != nil {
			return err
		}

		switch typ {
		case "CMD":
			cmdSlice := handleJSONArgs(args, req.attributes)
			if len(cmdSlice) == 0 {
				return errors.New("Missing command after HEALTHCHECK CMD")
			}

			if !req.attributes["json"] {
				typ = "CMD-SHELL"
			}

			healthcheck.Test = strslice.StrSlice(append([]string{typ}, cmdSlice...))
		default:
			return fmt.Errorf("Unknown type %#v in HEALTHCHECK (try CMD)", typ)
		}

		interval, err := parseOptInterval(flInterval)
		if err != nil {
			return err
		}
		healthcheck.Interval = interval

		timeout, err := parseOptInterval(flTimeout)
		if err != nil {
			return err
		}
		healthcheck.Timeout = timeout

		startPeriod, err := parseOptInterval(flStartPeriod)
		if err != nil {
			return err
		}
		healthcheck.StartPeriod = startPeriod

		if flRetries.Value != "" {
			retries, err := strconv.ParseInt(flRetries.Value, 10, 32)
			if err != nil {
				return err
			}
			if retries < 1 {
				return fmt.Errorf("--retries must be at least 1 (not %d)", retries)
			}
			healthcheck.Retries = int(retries)
		} else {
			healthcheck.Retries = 0
		}

		cmd.Health = &healthcheck
	}
	stage.AddCommand(cmd)
	return nil
}

func dispatchEntrypoint(state *dispatchState, c *command.EntrypointCommand, expand processWordFunc) error {
	runConfig := state.runConfig
	switch {
	case c.Discard:
		runConfig.Entrypoint = nil
	case c.PrependShell:
		cmd, err := processWordSingleOutputs(c.CmdLine, expand)
		if err != nil {
			return err
		}
		runConfig.Entrypoint = append(getShell(runConfig), cmd...)
	default:
		cmd, err := processWordSingleOutputs(c.CmdLine, expand)
		if err != nil {
			return err
		}
		runConfig.Entrypoint = cmd
	}

	if !state.cmdSet {
		runConfig.Cmd = nil
	}

	return state.builder.commit(state, fmt.Sprintf("ENTRYPOINT %q", runConfig.Entrypoint))
}

// ENTRYPOINT /usr/sbin/nginx
//
// Set the entrypoint to /usr/sbin/nginx. Will accept the CMD as the arguments
// to /usr/sbin/nginx. Uses the default shell if not in JSON format.
//
// Handles command processing similar to CMD and RUN, only req.runConfig.Entrypoint
// is initialized at newBuilder time instead of through argument parsing.
//
func parseEntrypoint(req parseRequest) error {
	if err := req.flags.Parse(); err != nil {
		return err
	}
	stage, err := req.state.CurrentStage()
	if err != nil {
		return err
	}

	cmd := &command.EntrypointCommand{
		OriginalSource: req.original,
	}

	parsed := handleJSONArgs(req.args, req.attributes)

	switch {
	case req.attributes["json"]:
		// ENTRYPOINT ["echo", "hi"]
		cmd.CmdLine = strslice.StrSlice(parsed)
	case len(parsed) == 0:
		// ENTRYPOINT []
		cmd.Discard = true
	default:
		// ENTRYPOINT echo hi
		cmd.CmdLine = strslice.StrSlice(parsed)
		cmd.PrependShell = true
	}
	stage.AddCommand(cmd)
	return nil
}

func dispatchExpose(state *dispatchState, c *command.ExposeCommand, expand processWordFunc) error {
	if state.runConfig.ExposedPorts == nil {
		state.runConfig.ExposedPorts = make(nat.PortSet)
	}
	ports, err := processWordManyOutputs(c.Ports, expand)
	if err != nil {
		return nil
	}
	for _, p := range ports {
		state.runConfig.ExposedPorts[nat.Port(p)] = struct{}{}
	}

	return state.builder.commit(state, "EXPOSE "+strings.Join(ports, " "))
}

// EXPOSE 6667/tcp 7000/tcp
//
// Expose ports for links and port mappings. This all ends up in
// req.runConfig.ExposedPorts for runconfig.
//
func parseExpose(req parseRequest) error {
	portsTab := req.args

	if len(req.args) == 0 {
		return errAtLeastOneArgument("EXPOSE")
	}

	if err := req.flags.Parse(); err != nil {
		return err
	}
	stage, err := req.state.CurrentStage()
	if err != nil {
		return err
	}

	ports, _, err := nat.ParsePortSpecs(portsTab)
	if err != nil {
		return err
	}

	// instead of using ports directly, we build a list of ports and sort it so
	// the order is consistent. This prevents cache burst where map ordering
	// changes between builds
	portList := make([]string, len(ports))
	var i int
	for port := range ports {
		portList[i] = string(port)
		i++
	}
	sort.Strings(portList)
	stage.AddCommand(&command.ExposeCommand{
		Ports:          portList,
		OriginalSource: req.original,
	})
	return nil
}

func dispatchUser(state *dispatchState, c *command.UserCommand, expand processWordFunc) error {
	user, err := processWordSingleOutput(c.User, expand)
	if err != nil {
		return err
	}
	state.runConfig.User = user
	return state.builder.commit(state, fmt.Sprintf("USER %v", user))
}

// USER foo
//
// Set the user to 'foo' for future commands and when running the
// ENTRYPOINT/CMD at container run time.
//
func parseUser(req parseRequest) error {
	if len(req.args) != 1 {
		return errExactlyOneArgument("USER")
	}

	if err := req.flags.Parse(); err != nil {
		return err
	}
	stage, err := req.state.CurrentStage()
	if err != nil {
		return err
	}
	stage.AddCommand(&command.UserCommand{
		User:           req.args[0],
		OriginalSource: req.original,
	})
	return nil
}

func dispatchVolume(state *dispatchState, c *command.VolumeCommand, expand processWordFunc) error {
	volumes, err := processWordSingleOutputs(c.Volumes, expand)
	if err != nil {
		return err
	}
	if state.runConfig.Volumes == nil {
		state.runConfig.Volumes = map[string]struct{}{}
	}
	for _, v := range volumes {
		state.runConfig.Volumes[v] = struct{}{}
	}
	return state.builder.commit(state, fmt.Sprintf("VOLUME %v", volumes))
}

// VOLUME /foo
//
// Expose the volume /foo for use. Will also accept the JSON array form.
//
func parseVolume(req parseRequest) error {
	if len(req.args) == 0 {
		return errAtLeastOneArgument("VOLUME")
	}

	if err := req.flags.Parse(); err != nil {
		return err
	}
	stage, err := req.state.CurrentStage()
	if err != nil {
		return err
	}

	cmd := &command.VolumeCommand{
		OriginalSource: req.original,
	}

	for _, v := range req.args {
		v = strings.TrimSpace(v)
		if v == "" {
			return errors.New("VOLUME specified can not be an empty string")
		}
		cmd.Volumes = append(cmd.Volumes, v)
	}
	stage.AddCommand(cmd)
	return nil

}

func dispatchStopSignal(state *dispatchState, c *command.StopSignalCommand, expand processWordFunc) error {
	sig, err := processWordSingleOutput(c.Sig, expand)
	if err != nil {
		return err
	}
	_, err = signal.ParseSignal(sig)
	if err != nil {
		return err
	}
	state.runConfig.StopSignal = sig
	return state.builder.commit(state, fmt.Sprintf("STOPSIGNAL %v", sig))
}

// STOPSIGNAL signal
//
// Set the signal that will be used to kill the container.
func parseStopSignal(req parseRequest) error {
	if len(req.args) != 1 {
		return errExactlyOneArgument("STOPSIGNAL")
	}
	stage, err := req.state.CurrentStage()
	if err != nil {
		return err
	}
	sig := req.args[0]

	cmd := &command.StopSignalCommand{
		Sig:            sig,
		OriginalSource: req.original,
	}
	stage.AddCommand(cmd)
	return nil

}

func dispatchArg(state *dispatchState, c *command.ArgCommand, expand processWordFunc) error {
	arg, err := processWordSingleOutput(c.Arg, expand)
	if err != nil {
		return err
	}
	return state.builder.commit(state, "ARG "+arg)
}

// ARG name[=value]
//
// Adds the variable foo to the trusted list of variables that can be passed
// to builder using the --build-arg flag for expansion/substitution or passing to 'run'.
// Dockerfile author may optionally set a default value of this variable.
func parseArg(req parseRequest) error {
	if len(req.args) != 1 {
		return errExactlyOneArgument("ARG")
	}

	var (
		name       string
		newValue   string
		hasDefault bool
	)

	arg := req.args[0]
	// 'arg' can just be a name or name-value pair. Note that this is different
	// from 'env' that handles the split of name and value at the parser level.
	// The reason for doing it differently for 'arg' is that we support just
	// defining an arg and not assign it a value (while 'env' always expects a
	// name-value pair). If possible, it will be good to harmonize the two.
	if strings.Contains(arg, "=") {
		parts := strings.SplitN(arg, "=", 2)
		if len(parts[0]) == 0 {
			return errBlankCommandNames("ARG")
		}

		name = parts[0]
		newValue = parts[1]
		hasDefault = true
	} else {
		name = arg
		hasDefault = false
	}

	var value *string
	if hasDefault {
		value = &newValue
	}
	req.buildArgs.AddArg(name, value)

	// Arg before FROM doesn't add a layer
	if len(req.state.Stages) == 0 {
		req.buildArgs.AddMetaArg(name, value)
		return nil
	}
	stage, err := req.state.CurrentStage()
	if err != nil {
		return err
	}
	stage.AddCommand(&command.ArgCommand{
		Arg:            arg,
		OriginalSource: req.original,
	})
	return nil
}

func dispatchShell(state *dispatchState, c *command.ShellCommand, expand processWordFunc) error {
	var err error
	state.runConfig.Shell, err = processWordSingleOutputs(c.Shell, expand)
	if err != nil {
		return err
	}
	return state.builder.commit(state, fmt.Sprintf("SHELL %v", state.runConfig.Shell))
}

// SHELL powershell -command
//
// Set the non-default shell to use.
func parseShell(req parseRequest) error {
	if err := req.flags.Parse(); err != nil {
		return err
	}
	shellSlice := handleJSONArgs(req.args, req.attributes)
	switch {
	case len(shellSlice) == 0:
		// SHELL []
		return errAtLeastOneArgument("SHELL")
	case req.attributes["json"]:
		// SHELL ["powershell", "-command"]
		stage, err := req.state.CurrentStage()
		if err != nil {
			return err
		}
		stage.AddCommand(&command.ShellCommand{
			Shell:          strslice.StrSlice(shellSlice),
			OriginalSource: req.original,
		})
		return nil
	default:
		// SHELL powershell -command - not JSON
		return errNotJSON("SHELL", req.original)
	}
}

func errAtLeastOneArgument(command string) error {
	return fmt.Errorf("%s requires at least one argument", command)
}

func errExactlyOneArgument(command string) error {
	return fmt.Errorf("%s requires exactly one argument", command)
}

func errAtLeastTwoArguments(command string) error {
	return fmt.Errorf("%s requires at least two arguments", command)
}

func errBlankCommandNames(command string) error {
	return fmt.Errorf("%s names can not be blank", command)
}

func errTooManyArguments(command string) error {
	return fmt.Errorf("Bad input to %s, too many arguments", command)
}
