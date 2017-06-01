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
	"strings"

	"reflect"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/builder"
	"github.com/docker/docker/builder/dockerfile/parser"
	"github.com/docker/docker/builder/dockerfile/typedcommand"
	"github.com/docker/docker/pkg/system"
	"github.com/docker/docker/runconfig/opts"
	"github.com/pkg/errors"
)

// Environment variable interpolation will happen on these statements only.
var replaceEnvAllowed = map[reflect.Type]bool{
	reflect.TypeOf((*typedcommand.EnvCommand)(nil)):        true,
	reflect.TypeOf((*typedcommand.LabelCommand)(nil)):      true,
	reflect.TypeOf((*typedcommand.AddCommand)(nil)):        true,
	reflect.TypeOf((*typedcommand.CopyCommand)(nil)):       true,
	reflect.TypeOf((*typedcommand.WorkdirCommand)(nil)):    true,
	reflect.TypeOf((*typedcommand.ExposeCommand)(nil)):     true,
	reflect.TypeOf((*typedcommand.VolumeCommand)(nil)):     true,
	reflect.TypeOf((*typedcommand.UserCommand)(nil)):       true,
	reflect.TypeOf((*typedcommand.StopSignalCommand)(nil)): true,
	reflect.TypeOf((*typedcommand.ArgCommand)(nil)):        true,
	reflect.TypeOf((*typedcommand.OnbuildCommand)(nil)):    true,
}

// Certain commands are allowed to have their args split into more
// words after env var replacements. Meaning:
//   ENV foo="123 456"
//   EXPOSE $foo
// should result in the same thing as:
//   EXPOSE 123 456
// and not treat "123 456" as a single word.
// Note that: EXPOSE "$foo" and EXPOSE $foo are not the same thing.
// Quotes will cause it to still be treated as single word.
var allowWordExpansion = map[reflect.Type]bool{
	reflect.TypeOf((*typedcommand.ExposeCommand)(nil)): true,
}

func formatStep(stepN int, stepTotal int) string {
	return fmt.Sprintf("%d/%d", stepN+1, stepTotal)
}

func (d *dispatcher) dispatch(cmd interface{}) error {
	if err := platformSupports(cmd); err != nil {
		buildsFailed.WithValues(metricsCommandNotSupportedError).Inc()
		return err
	}
	runConfigEnv := d.state.runConfig.Env
	envs := append(runConfigEnv, d.builder.buildArgs.FilterAllowed(runConfigEnv)...)
	processWord := createProcessWordFunc(d.shlex, reflect.TypeOf(cmd), envs)
	switch c := cmd.(type) {
	case *typedcommand.FromCommand:
		return d.dispatchFrom(c)
	case *typedcommand.EnvCommand:
		return d.dispatchEnv(c, processWord)
	case *typedcommand.MaintainerCommand:
		return d.dispatchMaintainer(c, processWord)
	case *typedcommand.LabelCommand:
		return d.dispatchLabel(c, processWord)
	case *typedcommand.AddCommand:
		return d.dispatchAdd(c, processWord)
	case *typedcommand.CopyCommand:
		return d.dispatchCopy(c, processWord)
	case *typedcommand.OnbuildCommand:
		return d.dispatchOnbuild(c, processWord)
	case *typedcommand.WorkdirCommand:
		return d.dispatchWorkdir(c, processWord)
	case *typedcommand.RunCommand:
		return d.dispatchRun(c, processWord)
	case *typedcommand.CmdCommand:
		return d.dispatchCmd(c, processWord)
	case *typedcommand.HealthCheckCommand:
		return d.dispatchHealthcheck(c, processWord)
	case *typedcommand.EntrypointCommand:
		return d.dispatchEntrypoint(c, processWord)
	case *typedcommand.ExposeCommand:
		return d.dispatchExpose(c, processWord)
	case *typedcommand.UserCommand:
		return d.dispatchUser(c, processWord)
	case *typedcommand.VolumeCommand:
		return d.dispatchVolume(c, processWord)
	case *typedcommand.StopSignalCommand:
		return d.dispatchStopSignal(c, processWord)
	case *typedcommand.ArgCommand:
		return d.dispatchInStageArg(c, processWord)
	case *typedcommand.ShellCommand:
		return d.dispatchShell(c, processWord)
	case *typedcommand.ResumeBuildCommand:
		return d.dispatchResumeBuild(c)
	}
	return errors.Errorf("unsupported command type: %v", reflect.TypeOf(cmd))
}

// dispatchState is a data object which is modified by dispatchers
type dispatchState struct {
	runConfig         *container.Config
	maintainer        string
	cmdSet            bool
	imageID           string
	baseImage         builder.Image
	stageName         string
	hasDispatchedFrom bool
}

type dispatcher struct {
	state   *dispatchState
	shlex   *ShellLex
	builder *Builder
	source  builder.Source
}

func newDispatchState() *dispatchState {
	return &dispatchState{runConfig: &container.Config{}}
}

func newDispatcher(builder *Builder, escapeToken rune, source builder.Source) *dispatcher {
	return &dispatcher{
		state:   newDispatchState(),
		shlex:   NewShellLex(escapeToken),
		builder: builder,
		source:  source,
	}
}
func (s *dispatcher) updateRunConfig() {
	s.state.runConfig.Image = s.state.imageID
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
}

// Add the default PATH to runConfig.ENV if one exists for the platform and there
// is no PATH set. Note that windows won't have one as it's set by HCS
func (s *dispatchState) setDefaultPath() {
	if system.DefaultPathEnv == "" {
		return
	}
	envMap := opts.ConvertKVStringsToMap(s.runConfig.Env)
	if _, ok := envMap["PATH"]; !ok {
		s.runConfig.Env = append(s.runConfig.Env, "PATH="+system.DefaultPathEnv)
	}
}

func handleOnBuildNode(ast *parser.Node, msg *bytes.Buffer) (*parser.Node, []string, error) {
	if ast.Next == nil {
		return nil, nil, errors.New("ONBUILD requires at least one argument")
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

func getDispatchArgsFromNode(ast *parser.Node, processFunc processWordFunc, msg *bytes.Buffer) ([]string, error) {
	args := []string{}
	for i := 0; ast.Next != nil; i++ {
		ast = ast.Next
		words, err := processFunc(ast.Value)
		if err != nil {
			return nil, err
		}
		args = append(args, words...)
		msg.WriteString(" " + ast.Value)
	}
	return args, nil
}

type processWordFunc func(string) ([]string, error)

func processWordSingleOutput(value string, f processWordFunc) (string, error) {
	vals, err := f(value)
	if err != nil {
		return "", err
	}
	if len(vals) != 1 {
		return "", errors.Errorf("output has %v values", len(vals))
	}
	return vals[0], nil
}
func processWordSingleOutputs(value []string, f processWordFunc) ([]string, error) {
	result := make([]string, len(value))
	for i, v := range value {
		r, err := processWordSingleOutput(v, f)
		if err != nil {
			return nil, err
		}
		result[i] = r
	}
	return result, nil
}
func processWordManyOutputs(value []string, f processWordFunc) ([]string, error) {
	result := []string{}
	for _, v := range value {
		r, err := f(v)
		if err != nil {
			return nil, err
		}
		result = append(result, r...)
	}
	return result, nil
}

func createProcessWordFunc(shlex *ShellLex, cmdType reflect.Type, envs []string) processWordFunc {
	switch {
	case !replaceEnvAllowed[cmdType]:
		return func(word string) ([]string, error) {
			return []string{word}, nil
		}
	case allowWordExpansion[cmdType]:
		return func(word string) ([]string, error) {
			return shlex.ProcessWords(word, envs)
		}
	default:
		return func(word string) ([]string, error) {
			word, err := shlex.ProcessWord(word, envs)
			return []string{word}, err
		}
	}
}
