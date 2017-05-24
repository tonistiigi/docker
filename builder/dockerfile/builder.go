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
	"github.com/docker/docker/builder/dockerfile/command"
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

	if err := checkDispatchDockerfile(dockerfile.AST); err != nil {
		buildsFailed.WithValues(metricsDockerfileSyntaxError).Inc()
		return nil, err
	}

	parseResult, err := b.parseDockerfileWithCancellation(dockerfile, source)
	if err != nil {
		return nil, err
	}

	if b.options.Target != "" && !parseResult.IsCurrentStage(b.options.Target) {
		buildsFailed.WithValues(metricsBuildTargetNotReachableError).Inc()
		return nil, errors.Errorf("failed to reach build target %s in Dockerfile", b.options.Target)
	}

	b.buildArgs.WarnOnUnusedBuildArgs(b.Stderr)

	dispatchState, err := b.dispatchDockerfileWithCancellation(parseResult, dockerfile.EscapeToken, source)
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
func (b *Builder) dispatchDockerfileWithCancellation(parseResult *command.ParsingResult, escapeToken rune, source builder.Source) (*dispatchState, error) {
	var state *dispatchState
	for _, stage := range parseResult.Stages {
		state = newDispatchState(b, escapeToken, source)
		for i, cmd := range stage.Commands {
			select {
			case <-b.clientCtx.Done():
				logrus.Debug("Builder: build cancelled!")
				fmt.Fprint(b.Stdout, "Build cancelled")
				buildsFailed.WithValues(metricsBuildCanceled).Inc()
				return nil, errors.New("Build cancelled")
			default:
				// Not cancelled yet, keep going...
			}
			sourceCode := "..."
			if src, ok := cmd.(command.WithSourceCode); ok {
				sourceCode = src.SourceCode()
			}
			if len(parseResult.Stages) > 1 {
				fmt.Fprintf(b.Stdout, "Stage %v: %v / %v %v", stage.Name, i+1, len(stage.Commands), sourceCode)
			} else {
				fmt.Fprintf(b.Stdout, "%v / %v %v", i+1, len(stage.Commands), sourceCode)
			}
			fmt.Fprintln(b.Stdout)

			if err := b.dispatch(state, cmd); err != nil {
				return nil, err
			}

			state.updateRunConfig()
			fmt.Fprintf(b.Stdout, " ---> %s\n", stringid.TruncateID(state.imageID))
			if b.options.Remove {
				b.clearTmp()
			}
		}
		if err := emitImageID(b.Aux, state); err != nil {
			return nil, err
		}
	}
	return state, nil
}
func (b *Builder) parseDockerfileWithCancellation(dockerfile *parser.Result, source builder.Source) (*command.ParsingResult, error) {
	state := &command.ParsingResult{}
	var err error
	for _, n := range dockerfile.AST.Children {
		select {
		case <-b.clientCtx.Done():
			logrus.Debug("Builder: build cancelled!")
			fmt.Fprint(b.Stdout, "Build cancelled")
			buildsFailed.WithValues(metricsBuildCanceled).Inc()
			return nil, errors.New("Build cancelled")
		default:
			// Not cancelled yet, keep going...
		}

		if n.Value == command.From && state.IsCurrentStage(b.options.Target) {
			break
		}

		opts := parsingOptions{
			state: state,
			node:  n,
		}
		if state, err = b.parse(opts); err != nil {
			if b.options.ForceRemove {
				b.clearTmp()
			}
			return nil, err
		}

	}

	return state, nil
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

	if err := checkDispatchDockerfile(dockerfile.AST); err != nil {
		return nil, err
	}
	// dispatchState := newDispatchState()
	// dispatchState.runConfig = config
	// return dispatchFromDockerfile(b, dockerfile, dispatchState)

	stage := command.BuildableStage{
		Name: "0",
		Commands: []interface{}{&command.ResumeBuildCommand{
			BaseConfig: config,
		}},
	}

	parseState := &command.ParsingResult{
		Stages: []command.BuildableStage{
			stage,
		},
	}
	b.buildArgs.ResetAllowed()
	parseState, err = parseFromDockerfile(b, dockerfile, parseState)
	if err != nil {
		return nil, err
	}
	res, err := b.dispatchDockerfileWithCancellation(parseState, dockerfile.EscapeToken, nil)

	if err != nil {
		return nil, err
	}
	return res.runConfig, nil
}

type resumeBuildCommand struct {
	baseCommand
	runConfig *container.Config
}

func (c *resumeBuildCommand) dispatch(state *dispatchState) error {
	c.builder.resetImageCache()
	state.runConfig = c.runConfig
	state.runConfig.OnBuild = []string{}

	return nil
}

func checkDispatchDockerfile(dockerfile *parser.Node) error {
	for _, n := range dockerfile.Children {
		if err := checkDispatch(n); err != nil {
			return errors.Wrapf(err, "Dockerfile parse error line %d", n.StartLine)
		}
	}
	return nil
}

func parseFromDockerfile(b *Builder, result *parser.Result, state *command.ParsingResult) (*command.ParsingResult, error) {
	ast := result.AST

	for _, n := range ast.Children {
		opts := parsingOptions{
			state: state,
			node:  n,
		}
		if _, err := b.parse(opts); err != nil {
			return nil, err
		}
	}
	return state, nil
}
