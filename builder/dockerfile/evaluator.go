// Package dockerfile is the evaluation step in the Dockerfile parse/evaluate pipeline.
//
// It incorporates a dispatch table based on the parser.Node values (see the
// parser package for more information) that are yielded from the parser itself.
// Calling newBuilder with the BuildOpts struct can be used to customize the
// experience for execution purposes only. Parsing is controlled in the parser
// package, and this division of responsibility should be respected.
//
// Please see the jump table targets for the actual invocations, most of which
// will call out to the functions in internals.go to deal with their tasks.
//
// ONBUILD is a special case, which is covered in the onbuild() func in
// dispatchers.go.
//
// The evaluator uses the concept of "steps", which are usually each processable
// line in the Dockerfile. Each step is numbered and certain actions are taken
// before and after each step, such as creating an image ID and removing temporary
// containers and images. Note that ONBUILD creates a kinda-sorta "sub run" which
// includes its own set of steps (usually only one of them).
package dockerfile

import (
	"bytes"
	"fmt"
	"runtime"
	"strings"

	"reflect"

	"strconv"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/builder"
	"github.com/docker/docker/builder/dockerfile/instructions"
	"github.com/docker/docker/builder/dockerfile/parser"
	"github.com/docker/docker/pkg/system"
	"github.com/docker/docker/runconfig/opts"
	"github.com/pkg/errors"
)

func formatStep(stepN int, stepTotal int) string {
	return fmt.Sprintf("%d/%d", stepN+1, stepTotal)
}

func dispatch(d *dispatchRequest, cmd interface{}) error {
	if err := platformSupports(cmd); err != nil {
		buildsFailed.WithValues(metricsCommandNotSupportedError).Inc()
		return validationError{err}
	}
	runConfigEnv := d.state.runConfig.Env
	envs := append(runConfigEnv, d.state.buildArgs.FilterAllowed(runConfigEnv)...)

	if ex, ok := cmd.(instructions.SupportsSingleWordExpansion); ok {
		err := ex.Expand(func(word string) (string, error) {
			return d.shlex.ProcessWord(word, envs)
		})
		if err != nil {
			return validationError{err}
		}
	}

	if d.builder.options.ForceRemove {
		defer d.builder.containerManager.RemoveAll(d.builder.Stdout)
	}

	switch c := cmd.(type) {
	case *instructions.EnvCommand:
		return dispatchEnv(d, c)
	case *instructions.MaintainerCommand:
		return dispatchMaintainer(d, c)
	case *instructions.LabelCommand:
		return dispatchLabel(d, c)
	case *instructions.AddCommand:
		return dispatchAdd(d, c)
	case *instructions.CopyCommand:
		return dispatchCopy(d, c)
	case *instructions.OnbuildCommand:
		return dispatchOnbuild(d, c)
	case *instructions.WorkdirCommand:
		return dispatchWorkdir(d, c)
	case *instructions.RunCommand:
		return dispatchRun(d, c)
	case *instructions.CmdCommand:
		return dispatchCmd(d, c)
	case *instructions.HealthCheckCommand:
		return dispatchHealthcheck(d, c)
	case *instructions.EntrypointCommand:
		return dispatchEntrypoint(d, c)
	case *instructions.ExposeCommand:
		return dispatchExpose(d, c, envs)
	case *instructions.UserCommand:
		return dispatchUser(d, c)
	case *instructions.VolumeCommand:
		return dispatchVolume(d, c)
	case *instructions.StopSignalCommand:
		return dispatchStopSignal(d, c)
	case *instructions.ArgCommand:
		return dispatchArg(d, c)
	case *instructions.ShellCommand:
		return dispatchShell(d, c)
	case *instructions.ResumeBuildCommand:
		return dispatchResumeBuild(d, c)
	}
	return validationError{errors.Errorf("unsupported command type: %v", reflect.TypeOf(cmd))}
}

// dispatchState is a data object which is modified by dispatchers
type dispatchState struct {
	runConfig  *container.Config
	maintainer string
	cmdSet     bool
	imageID    string
	baseImage  builder.Image
	stageName  string
	buildArgs  *buildArgs
}

func newDispatchState(baseArgs *buildArgs) *dispatchState {
	args := baseArgs.Clone()
	args.ResetAllowed()
	return &dispatchState{runConfig: &container.Config{}, buildArgs: args}
}

type previousStagesResults struct {
	flat    []*container.Config
	indexed map[string]*container.Config
}

func newPreviousStagesResults() *previousStagesResults {
	return &previousStagesResults{
		indexed: make(map[string]*container.Config),
	}
}

func (p *previousStagesResults) getByName(name string) (*container.Config, bool) {
	c, ok := p.indexed[strings.ToLower(name)]
	return c, ok
}

func (p *previousStagesResults) validateIndex(i int) error {
	if i == len(p.flat) {
		return errors.New("refers to current build stage")
	}
	if i < 0 || i > len(p.flat) {
		return errors.New("index out of bounds")
	}
	return nil
}

func (p *previousStagesResults) get(nameOrIndex string) (*container.Config, error) {
	if c, ok := p.getByName(nameOrIndex); ok {
		return c, nil
	}
	ix, err := strconv.ParseInt(nameOrIndex, 10, 0)
	if err != nil {
		return nil, nil
	}
	if err := p.validateIndex(int(ix)); err != nil {
		return nil, err
	}
	return p.flat[ix], nil
}

func (p *previousStagesResults) checkStageNameAvailable(name string) error {
	if name != "" {
		if _, ok := p.getByName(name); ok {
			return errors.Errorf("%s stage name already used", name)
		}
	}
	return nil
}

func (p *previousStagesResults) commitStage(name string, config *container.Config) error {
	if name != "" {
		if _, ok := p.getByName(name); ok {
			return errors.Errorf("%s stage name already used", name)
		}
		p.indexed[strings.ToLower(name)] = config
	}
	p.flat = append(p.flat, config)
	return nil
}

type dispatchRequest struct {
	state           *dispatchState
	shlex           *ShellLex
	builder         *Builder
	source          builder.Source
	previousResults *previousStagesResults
}

func newDispatchRequest(builder *Builder, escapeToken rune, source builder.Source, buildArgs *buildArgs, previousResults *previousStagesResults) *dispatchRequest {
	return &dispatchRequest{
		state:           newDispatchState(buildArgs),
		shlex:           NewShellLex(escapeToken),
		builder:         builder,
		source:          source,
		previousResults: previousResults,
	}
}

func (d *dispatchRequest) commitStage() error {
	return d.previousResults.commitStage(d.state.stageName, d.state.runConfig)
}

func (s *dispatchState) updateRunConfig() {
	s.runConfig.Image = s.imageID
}

// hasFromImage returns true if the builder has processed a `FROM <image>` line
func (s *dispatchState) hasFromImage() bool {
	return s.imageID != "" || (s.baseImage != nil && s.baseImage.ImageID() == "")
}

func (s *dispatchState) isCurrentStage(target string) bool {
	if target == "" {
		return false
	}
	return strings.EqualFold(s.stageName, target)
}

func (s *dispatchState) beginStage(stageName string, image builder.Image) {
	s.stageName = stageName
	s.imageID = image.ImageID()

	if image.RunConfig() != nil {
		s.runConfig = image.RunConfig()
	} else {
		s.runConfig = &container.Config{}
	}
	s.baseImage = image
	s.setDefaultPath()
	s.runConfig.OpenStdin = false
	s.runConfig.StdinOnce = false
}

// Add the default PATH to runConfig.ENV if one exists for the platform and there
// is no PATH set. Note that Windows containers on Windows won't have one as it's set by HCS
func (s *dispatchState) setDefaultPath() {
	// TODO @jhowardmsft LCOW Support - This will need revisiting later
	platform := runtime.GOOS
	if system.LCOWSupported() {
		platform = "linux"
	}
	if system.DefaultPathEnv(platform) == "" {
		return
	}
	envMap := opts.ConvertKVStringsToMap(s.runConfig.Env)
	if _, ok := envMap["PATH"]; !ok {
		s.runConfig.Env = append(s.runConfig.Env, "PATH="+system.DefaultPathEnv(platform))
	}
}

func handleOnBuildNode(ast *parser.Node, msg *bytes.Buffer) (*parser.Node, []string, error) {
	if ast.Next == nil {
		return nil, nil, validationError{errors.New("ONBUILD requires at least one argument")}
	}
	ast = ast.Next.Children[0]
	msg.WriteString(" " + ast.Value + formatFlags(ast.Flags))
	return ast, []string{ast.Value}, nil
}

func formatFlags(flags []string) string {
	if len(flags) > 0 {
		return " " + strings.Join(flags, " ")
	}
	return ""
}
