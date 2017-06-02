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
	"github.com/docker/docker/builder/dockerfile/instructions"
	"github.com/docker/docker/builder/dockerfile/parser"
	"github.com/docker/docker/pkg/system"
	"github.com/docker/docker/runconfig/opts"
	"github.com/pkg/errors"
)

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
	if ex, ok := cmd.(instructions.SupportsMultiWordExpansion); ok {
		ex.Expand(func(word string) ([]string, error) {
			return d.shlex.ProcessWords(word, envs)
		})
	}
	if ex, ok := cmd.(instructions.SupportsSingleWordExpansion); ok {
		ex.Expand(func(word string) (string, error) {
			return d.shlex.ProcessWord(word, envs)
		})
	}

	switch c := cmd.(type) {
	case *instructions.FromCommand:
		return d.dispatchFrom(c)
	case *instructions.EnvCommand:
		return d.dispatchEnv(c)
	case *instructions.MaintainerCommand:
		return d.dispatchMaintainer(c)
	case *instructions.LabelCommand:
		return d.dispatchLabel(c)
	case *instructions.AddCommand:
		return d.dispatchAdd(c)
	case *instructions.CopyCommand:
		return d.dispatchCopy(c)
	case *instructions.OnbuildCommand:
		return d.dispatchOnbuild(c)
	case *instructions.WorkdirCommand:
		return d.dispatchWorkdir(c)
	case *instructions.RunCommand:
		return d.dispatchRun(c)
	case *instructions.CmdCommand:
		return d.dispatchCmd(c)
	case *instructions.HealthCheckCommand:
		return d.dispatchHealthcheck(c)
	case *instructions.EntrypointCommand:
		return d.dispatchEntrypoint(c)
	case *instructions.ExposeCommand:
		return d.dispatchExpose(c)
	case *instructions.UserCommand:
		return d.dispatchUser(c)
	case *instructions.VolumeCommand:
		return d.dispatchVolume(c)
	case *instructions.StopSignalCommand:
		return d.dispatchStopSignal(c)
	case *instructions.ArgCommand:
		if d.state.hasDispatchedFrom {
			return d.dispatchInStageArg(c)
		} else {
			return d.dispatchMetaArg(c)
		}
	case *instructions.ShellCommand:
		return d.dispatchShell(c)
	case *instructions.ResumeBuildCommand:
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
	s.runConfig.OpenStdin = false
	s.runConfig.StdinOnce = false
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
