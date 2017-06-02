package dockerfile

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"strings"

	"github.com/Sirupsen/logrus"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/backend"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/builder"
	"github.com/docker/docker/builder/dockerfile/instructions"
	"github.com/docker/docker/builder/dockerfile/parser"
	"github.com/docker/docker/builder/remotecontext"
	"github.com/docker/docker/pkg/streamformatter"
	"github.com/docker/docker/pkg/stringid"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
	"golang.org/x/sync/syncmap"
)

var validCommitCommands = map[string]bool{
	"cmd":         true,
	"entrypoint":  true,
	"healthcheck": true,
	"env":         true,
	"expose":      true,
	"label":       true,
	"onbuild":     true,
	"user":        true,
	"volume":      true,
	"workdir":     true,
}

var defaultLogConfig = container.LogConfig{Type: "none"}

// BuildManager is shared across all Builder objects
type BuildManager struct {
	backend   builder.Backend
	pathCache pathCache // TODO: make this persistent
}

// NewBuildManager creates a BuildManager
func NewBuildManager(b builder.Backend) *BuildManager {
	return &BuildManager{
		backend:   b,
		pathCache: &syncmap.Map{},
	}
}

// Build starts a new build from a BuildConfig
func (bm *BuildManager) Build(ctx context.Context, config backend.BuildConfig) (*builder.Result, error) {
	buildsTriggered.Inc()
	if config.Options.Dockerfile == "" {
		config.Options.Dockerfile = builder.DefaultDockerfileName
	}

	source, dockerfile, err := remotecontext.Detect(config)
	if err != nil {
		return nil, err
	}
	if source != nil {
		defer func() {
			if err := source.Close(); err != nil {
				logrus.Debugf("[BUILDER] failed to remove temporary context: %v", err)
			}
		}()
	}

	builderOptions := builderOptions{
		Options:        config.Options,
		ProgressWriter: config.ProgressWriter,
		Backend:        bm.backend,
		PathCache:      bm.pathCache,
	}
	return newBuilder(ctx, builderOptions).build(source, dockerfile)
}

// builderOptions are the dependencies required by the builder
type builderOptions struct {
	Options        *types.ImageBuildOptions
	Backend        builder.Backend
	ProgressWriter backend.ProgressWriter
	PathCache      pathCache
}

// Builder is a Dockerfile builder
// It implements the builder.Backend interface.
type Builder struct {
	options *types.ImageBuildOptions

	Stdout io.Writer
	Stderr io.Writer
	Aux    *streamformatter.AuxFormatter
	Output io.Writer

	docker    builder.Backend
	clientCtx context.Context

	tmpContainers map[string]struct{}
	buildStages   *buildStages
	disableCommit bool
	cacheBusted   bool
	buildArgs     *buildArgs
	imageCache    builder.ImageCache
	imageSources  *imageSources
	pathCache     pathCache
}

// newBuilder creates a new Dockerfile builder from an optional dockerfile and a Options.
func newBuilder(clientCtx context.Context, options builderOptions) *Builder {
	config := options.Options
	if config == nil {
		config = new(types.ImageBuildOptions)
	}
	b := &Builder{
		clientCtx:     clientCtx,
		options:       config,
		Stdout:        options.ProgressWriter.StdoutFormatter,
		Stderr:        options.ProgressWriter.StderrFormatter,
		Aux:           options.ProgressWriter.AuxFormatter,
		Output:        options.ProgressWriter.Output,
		docker:        options.Backend,
		tmpContainers: map[string]struct{}{},
		buildArgs:     newBuildArgs(config.BuildArgs),
		buildStages:   newBuildStages(),
		imageSources:  newImageSources(clientCtx, options),
		pathCache:     options.PathCache,
	}
	return b
}

func (b *Builder) resetImageCache() {
	if icb, ok := b.docker.(builder.ImageCacheBuilder); ok {
		b.imageCache = icb.MakeImageCache(b.options.CacheFrom)
	}
	b.cacheBusted = false
}

// Build runs the Dockerfile builder by parsing the Dockerfile and executing
// the instructions from the file.
func (b *Builder) build(source builder.Source, dockerfile *parser.Result) (*builder.Result, error) {
	defer b.imageSources.Unmount()

	addNodesForLabelOption(dockerfile.AST, b.options.Labels)

	stages, metaArgs, err := instructions.Parse(dockerfile.AST)
	if err != nil {
		if strings.Index(err.Error(), "unknown instruction:") == 0 {
			buildsFailed.WithValues(metricsUnknownInstructionError).Inc()
		}
		return nil, err
	}
	if b.options.Target != "" {
		found, targetIx := instructions.HasStage(stages, b.options.Target)
		if !found {
			buildsFailed.WithValues(metricsBuildTargetNotReachableError).Inc()
			return nil, errors.Errorf("failed to reach build target %s in Dockerfile", b.options.Target)
		}
		stages = stages[:targetIx+1]
	}

	b.buildArgs.WarnOnUnusedBuildArgs(b.Stderr)

	dispatchState, err := b.dispatchDockerfileWithCancellation(stages, metaArgs, dockerfile.EscapeToken, source)
	if err != nil {
		return nil, err
	}
	if dispatchState.imageID == "" {
		buildsFailed.WithValues(metricsDockerfileEmptyError).Inc()
		return nil, errors.New("No image was generated. Is your Dockerfile empty?")
	}
	return &builder.Result{ImageID: dispatchState.imageID, FromImage: dispatchState.baseImage}, nil
}

func emitImageID(aux *streamformatter.AuxFormatter, state *dispatchState) error {
	if aux == nil || state.imageID == "" {
		return nil
	}
	return aux.Emit(types.BuildResult{ID: state.imageID})
}
func convertMapToEnvs(m map[string]string) []string {
	result := []string{}
	for k, v := range m {
		result = append(result, k+"="+v)
	}
	return result
}
func (b *Builder) dispatchDockerfileWithCancellation(parseResult []instructions.BuildableStage, metaArgs []instructions.ArgCommand, escapeToken rune, source builder.Source) (*dispatchState, error) {
	var stageBuilder *stageBuilder

	totalCommands := len(metaArgs)
	currentCommandIndex := 0
	for _, stage := range parseResult {
		totalCommands += len(stage.Commands)
	}
	for _, meta := range metaArgs {
		shlex := NewShellLex(escapeToken)
		envs := convertMapToEnvs(b.buildArgs.GetAllAllowed())
		if err := meta.Expand(func(word string) (string, error) {
			return shlex.ProcessWord(word, envs)
		}); err != nil {
			return nil, err
		}
		b.buildArgs.AddArg(meta.Name, meta.Value)
		b.buildArgs.AddMetaArg(meta.Name, meta.Value)

		fmt.Fprintf(b.Stdout, "%v / %v %v", currentCommandIndex, totalCommands, &meta)
		currentCommandIndex++
		fmt.Fprintln(b.Stdout)
	}

	for _, stage := range parseResult {
		stageBuilder = newStageBuilder(b, escapeToken, source)
		for _, cmd := range stage.Commands {
			select {
			case <-b.clientCtx.Done():
				logrus.Debug("Builder: build cancelled!")
				fmt.Fprint(b.Stdout, "Build cancelled")
				buildsFailed.WithValues(metricsBuildCanceled).Inc()
				return nil, errors.New("Build cancelled")
			default:
				// Not cancelled yet, keep going...
			}

			fmt.Fprintf(b.Stdout, "%v / %v %v", currentCommandIndex, totalCommands, cmd)
			currentCommandIndex++
			fmt.Fprintln(b.Stdout)

			if err := stageBuilder.dispatch(cmd); err != nil {
				return nil, err
			}

			stageBuilder.updateRunConfig()
			fmt.Fprintf(b.Stdout, " ---> %s\n", stringid.TruncateID(stageBuilder.state.imageID))
			if b.options.Remove {
				b.clearTmp()
			}
		}
		if err := emitImageID(b.Aux, stageBuilder.state); err != nil {
			return nil, err
		}
	}
	return stageBuilder.state, nil
}

func addNodesForLabelOption(dockerfile *parser.Node, labels map[string]string) {
	if len(labels) == 0 {
		return
	}

	node := parser.NodeFromLabels(labels)
	dockerfile.Children = append(dockerfile.Children, node)
}

// BuildFromConfig builds directly from `changes`, treating it as if it were the contents of a Dockerfile
// It will:
// - Call parse.Parse() to get an AST root for the concatenated Dockerfile entries.
// - Do build by calling builder.dispatch() to call all entries' handling routines
//
// BuildFromConfig is used by the /commit endpoint, with the changes
// coming from the query parameter of the same name.
//
// TODO: Remove?
func BuildFromConfig(config *container.Config, changes []string) (*container.Config, error) {
	if len(changes) == 0 {
		return config, nil
	}

	b := newBuilder(context.Background(), builderOptions{})

	dockerfile, err := parser.Parse(bytes.NewBufferString(strings.Join(changes, "\n")))
	if err != nil {
		return nil, err
	}

	// ensure that the commands are valid
	for _, n := range dockerfile.AST.Children {
		if !validCommitCommands[n.Value] {
			return nil, fmt.Errorf("%s is not a valid change command", n.Value)
		}
	}

	b.Stdout = ioutil.Discard
	b.Stderr = ioutil.Discard
	b.disableCommit = true

	// dispatchState := newDispatchState()
	// dispatchState.runConfig = config
	// return dispatchFromDockerfile(b, dockerfile, dispatchState)

	stage := instructions.BuildableStage{
		Name: "0",
		Commands: []interface{}{&instructions.ResumeBuildCommand{
			BaseConfig: config,
		}},
	}

	for _, n := range dockerfile.AST.Children {
		cmd, err := instructions.ParseCommand(n)
		if err != nil {
			if strings.Index(err.Error(), "unknown instruction:") == 0 {
				buildsFailed.WithValues(metricsUnknownInstructionError).Inc()
			}
			return nil, err
		}
		stage.AddCommand(cmd)
	}

	parseState := []instructions.BuildableStage{
		stage,
	}
	b.buildArgs.ResetAllowed()
	res, err := b.dispatchDockerfileWithCancellation(parseState, nil, dockerfile.EscapeToken, nil)

	if err != nil {
		return nil, err
	}
	return res.runConfig, nil
}
