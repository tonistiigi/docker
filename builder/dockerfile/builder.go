package dockerfile

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/docker/distribution/reference"
	apierrors "github.com/docker/docker/api/errors"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/backend"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/builder"
	"github.com/docker/docker/builder/dockerfile/parser"
	"github.com/docker/docker/image"
	"github.com/docker/docker/pkg/archive"
	"github.com/docker/docker/pkg/httputils"
	"github.com/docker/docker/pkg/stringid"
	"github.com/docker/docker/pkg/urlutil"
	perrors "github.com/pkg/errors"
	"golang.org/x/net/context"
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

// BuiltinAllowedBuildArgs is list of built-in allowed build args
var BuiltinAllowedBuildArgs = map[string]bool{
	"HTTP_PROXY":  true,
	"http_proxy":  true,
	"HTTPS_PROXY": true,
	"https_proxy": true,
	"FTP_PROXY":   true,
	"ftp_proxy":   true,
	"NO_PROXY":    true,
	"no_proxy":    true,
}

var defaultLogConfig = container.LogConfig{Type: "none"}

// Builder is a Dockerfile builder
// It implements the builder.Backend interface.
type Builder struct {
	options *types.ImageBuildOptions

	Stdout io.Writer
	Stderr io.Writer
	Output io.Writer

	docker    builder.Backend
	remote    builder.Remote
	clientCtx context.Context
	cancel    context.CancelFunc
	fsCache   *FSCache

	dockerfile       *parser.Node
	runConfig        *container.Config // runconfig for cmd, run, entrypoint etc.
	flags            *BFlags
	tmpContainers    map[string]struct{}
	image            string         // imageID
	imageContexts    *imageContexts // helper for storing contexts from builds
	noBaseImage      bool
	maintainer       string
	cmdSet           bool
	disableCommit    bool
	cacheBusted      bool
	allowedBuildArgs map[string]*string  // list of build-time args that are allowed for expansion/substitution and passing to commands in 'run'.
	allBuildArgs     map[string]struct{} // list of all build-time args found during parsing of the Dockerfile
	directive        parser.Directive

	// TODO: remove once docker.Commit can receive a tag
	id string

	imageCache builder.ImageCache
	from       builder.Image
}

// BuildManager implements builder.Backend and is shared across all Builder objects.
type BuildManager struct {
	backend          builder.Backend
	pathCache        *pathCache // TODO: make this persistent
	fsCache          *FSCache
	sessionTransport *ClientSessionTransport
	once             sync.Once
}

// NewBuildManager creates a BuildManager.
func NewBuildManager(b builder.Backend) *BuildManager {
	bm := &BuildManager{
		backend:   b,
		pathCache: &pathCache{},
	}

	tmpdir, err := ioutil.TempDir("", "fscache")
	if err != nil {
		logrus.Error(perrors.Wrap(err, "failed to create tmp directory"))
	}

	fsCache, err := NewFSCache(FSCacheOpt{
		Backend: &tmpCacheBackend{tmpdir},
	})
	if err != nil {
		logrus.Error(err) // TODO: FIX!
	}
	bm.fsCache = fsCache
	cst := NewClientSessionTransport()
	fsCache.RegisterTransport(ClientSessionTransportName, cst)
	bm.sessionTransport = cst
	return bm
}

func (bm *BuildManager) SyncFrom(ctx context.Context, id RemoteIdentifier) (builder.Remote, error) {
	return bm.fsCache.SyncFrom(ctx, id)
}

// BuildFromContext builds a new image from a given context.
func (bm *BuildManager) BuildFromContext(ctx context.Context, src io.ReadCloser, buildOptions *types.ImageBuildOptions, pg backend.ProgressWriter) (string, error) {
	if buildOptions.Squash && !bm.backend.HasExperimental() {
		return "", apierrors.NewBadRequestError(errors.New("squash is only supported with experimental mode"))
	}
	if buildOptions.Dockerfile == "" {
		buildOptions.Dockerfile = builder.DefaultDockerfileName
	}

	remote, dockerfile, err := bm.DetectRemoteContext(ctx, buildOptions.RemoteContext, buildOptions.Dockerfile, src, pg.ProgressReaderFunc)
	if err != nil {
		return "", err
	}

	if remote != nil {
		defer func() {
			if err := remote.Close(); err != nil {
				logrus.Debugf("[BUILDER] failed to remove temporary context: %v", err)
			}
		}()
	}

	b, err := NewBuilder(ctx, buildOptions, bm.backend, remote, dockerfile)
	if err != nil {
		return "", err
	}
	b.imageContexts.cache = bm.pathCache
	b.fsCache = bm.fsCache
	return b.build(pg.StdoutFormatter, pg.StderrFormatter, pg.Output)
}

// DetectContextFromRemoteURL returns a context and in certain cases the name of the dockerfile to be used
// irrespective of user input.
// progressReader is only used if remoteURL is actually a URL (not empty, and not a Git endpoint).
func (bm *BuildManager) DetectContextFromRemoteURL(r io.ReadCloser, remoteURL string, createProgressReader func(in io.ReadCloser) io.ReadCloser) (context builder.ModifiableContext, dockerfileName string, err error) {
	switch {
	case remoteURL == "":
		context, err = builder.MakeTarSumContext(r)
	case strings.HasPrefix(remoteURL, "session://"):
		// context, err = makeSessionContext(remoteURL[10:])
		return nil, "", nil
	case urlutil.IsGitURL(remoteURL):
		context, err = builder.MakeGitContext(remoteURL)
	case urlutil.IsURL(remoteURL):
		context, err = builder.MakeRemoteContext(remoteURL, map[string]func(io.ReadCloser) (io.ReadCloser, error){
			httputils.MimeTypes.TextPlain: func(rc io.ReadCloser) (io.ReadCloser, error) {
				dockerfile, err := ioutil.ReadAll(rc)
				if err != nil {
					return nil, err
				}

				// dockerfileName is set to signal that the remote was interpreted as a single Dockerfile, in which case the caller
				// should use dockerfileName as the new name for the Dockerfile, irrespective of any other user input.
				dockerfileName = builder.DefaultDockerfileName

				// TODO: return a context without tarsum
				r, err := archive.Generate(dockerfileName, string(dockerfile))
				if err != nil {
					return nil, err
				}

				return ioutil.NopCloser(r), nil
			},
			// fallback handler (tar context)
			"": func(rc io.ReadCloser) (io.ReadCloser, error) {
				return createProgressReader(rc), nil
			},
		})
	default:
		err := fmt.Errorf("remoteURL (%s) could not be recognized as URL", remoteURL)
		if err != nil {
			return nil, "", err
		}
	}
	return
}

// NewBuilder creates a new Dockerfile builder from an optional dockerfile and a Config.
// If dockerfile is nil, the Dockerfile specified by Config.DockerfileName,
// will be read from the Context passed to Build().
func NewBuilder(clientCtx context.Context, config *types.ImageBuildOptions, backend builder.Backend, remote builder.Remote, dockerfile io.ReadCloser) (b *Builder, err error) {
	if config == nil {
		config = new(types.ImageBuildOptions)
	}
	ctx, cancel := context.WithCancel(clientCtx)
	b = &Builder{
		clientCtx:        ctx,
		cancel:           cancel,
		options:          config,
		Stdout:           os.Stdout,
		Stderr:           os.Stderr,
		docker:           backend,
		remote:           remote,
		runConfig:        new(container.Config),
		tmpContainers:    map[string]struct{}{},
		id:               stringid.GenerateNonCryptoID(),
		allowedBuildArgs: make(map[string]*string),
		allBuildArgs:     make(map[string]struct{}),
		directive: parser.Directive{
			EscapeSeen:           false,
			LookingForDirectives: true,
		},
	}
	b.imageContexts = &imageContexts{b: b}

	parser.SetEscapeToken(parser.DefaultEscapeToken, &b.directive) // Assume the default token for escape

	if dockerfile != nil {
		if err := b.parseDockerfile(dockerfile); err != nil {
			return nil, err
		}
	}

	return b, nil
}

func (b *Builder) resetImageCache() {
	if icb, ok := b.docker.(builder.ImageCacheBuilder); ok {
		b.imageCache = icb.MakeImageCache(b.options.CacheFrom)
	}
	b.noBaseImage = false
	b.cacheBusted = false
}

// sanitizeRepoAndTags parses the raw "t" parameter received from the client
// to a slice of repoAndTag.
// It also validates each repoName and tag.
func sanitizeRepoAndTags(names []string) ([]reference.Named, error) {
	var (
		repoAndTags []reference.Named
		// This map is used for deduplicating the "-t" parameter.
		uniqNames = make(map[string]struct{})
	)
	for _, repo := range names {
		if repo == "" {
			continue
		}

		ref, err := reference.ParseNormalizedNamed(repo)
		if err != nil {
			return nil, err
		}

		if _, isCanonical := ref.(reference.Canonical); isCanonical {
			return nil, errors.New("build tag cannot contain a digest")
		}

		ref = reference.TagNameOnly(ref)

		nameWithTag := ref.String()

		if _, exists := uniqNames[nameWithTag]; !exists {
			uniqNames[nameWithTag] = struct{}{}
			repoAndTags = append(repoAndTags, ref)
		}
	}
	return repoAndTags, nil
}

func (b *Builder) processLabels() error {
	if len(b.options.Labels) == 0 {
		return nil
	}

	var labels []string
	for k, v := range b.options.Labels {
		labels = append(labels, fmt.Sprintf("%q='%s'", k, v))
	}
	// Sort the label to have a repeatable order
	sort.Strings(labels)

	line := "LABEL " + strings.Join(labels, " ")
	_, node, err := parser.ParseLine(line, &b.directive, false)
	if err != nil {
		return err
	}
	b.dockerfile.Children = append(b.dockerfile.Children, node)

	return nil
}

// build runs the Dockerfile builder from a context and a docker object that allows to make calls
// to Docker.
//
// This will (barring errors):
//
// * read the dockerfile from context
// * parse the dockerfile if not already parsed
// * walk the AST and execute it by dispatching to handlers. If Remove
//   or ForceRemove is set, additional cleanup around containers happens after
//   processing.
// * Tag image, if applicable.
// * Print a happy message and return the image ID.
//
func (b *Builder) build(stdout io.Writer, stderr io.Writer, out io.Writer) (string, error) {
	defer b.imageContexts.unmount()

	b.Stdout = stdout
	b.Stderr = stderr
	b.Output = out

	// TODO: remove this: read dockerfile from request, mode to detect
	if b.fsCache != nil && strings.HasPrefix(b.options.RemoteContext, "session://") {
		st := time.Now()
		ctx, err := b.fsCache.SyncFrom(context.Background(), NewClientSessionIdentifier(
			b.options.RemoteContext[len("session://"):], []string{"/"}))
		if err != nil {
			return "", err
		}

		b.remote = ctx
		defer ctx.Close()
		logrus.Debugf("sync-time: %v", time.Since(st))
	}

	repoAndTags, err := sanitizeRepoAndTags(b.options.Tags)
	if err != nil {
		return "", err
	}

	if err := b.processLabels(); err != nil {
		return "", err
	}

	var shortImgID string
	total := len(b.dockerfile.Children)
	for _, n := range b.dockerfile.Children {
		if err := b.checkDispatch(n, false); err != nil {
			return "", perrors.Wrapf(err, "Dockerfile parse error line %d", n.StartLine)
		}
	}

	for i, n := range b.dockerfile.Children {
		select {
		case <-b.clientCtx.Done():
			logrus.Debug("Builder: build cancelled!")
			fmt.Fprint(b.Stdout, "Build cancelled")
			return "", errors.New("Build cancelled")
		default:
			// Not cancelled yet, keep going...
		}

		if err := b.dispatch(i, total, n); err != nil {
			if b.options.ForceRemove {
				b.clearTmp()
			}
			return "", err
		}

		shortImgID = stringid.TruncateID(b.image)
		fmt.Fprintf(b.Stdout, " ---> %s\n", shortImgID)
		if b.options.Remove {
			b.clearTmp()
		}
	}

	b.warnOnUnusedBuildArgs()

	if b.image == "" {
		return "", errors.New("No image was generated. Is your Dockerfile empty?")
	}

	if b.options.Squash {
		var fromID string
		if b.from != nil {
			fromID = b.from.ImageID()
		}
		b.image, err = b.docker.SquashImage(b.image, fromID)
		if err != nil {
			return "", perrors.Wrap(err, "error squashing image")
		}
	}

	imageID := image.ID(b.image)
	for _, rt := range repoAndTags {
		if err := b.docker.TagImageWithReference(imageID, rt); err != nil {
			return "", err
		}
	}

	fmt.Fprintf(b.Stdout, "Successfully built %s\n", shortImgID)
	return b.image, nil
}

// check if there are any leftover build-args that were passed but not
// consumed during build. Print a warning, if there are any.
func (b *Builder) warnOnUnusedBuildArgs() {
	leftoverArgs := []string{}
	for arg := range b.options.BuildArgs {
		if _, ok := b.allBuildArgs[arg]; !ok {
			leftoverArgs = append(leftoverArgs, arg)
		}
	}

	if len(leftoverArgs) > 0 {
		fmt.Fprintf(b.Stderr, "[Warning] One or more build-args %v were not consumed\n", leftoverArgs)
	}
}

// Cancel cancels an ongoing Dockerfile build.
func (b *Builder) Cancel() {
	b.cancel()
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
	b, err := NewBuilder(context.Background(), nil, nil, nil, nil)
	if err != nil {
		return nil, err
	}

	ast, err := parser.Parse(bytes.NewBufferString(strings.Join(changes, "\n")), &b.directive)
	if err != nil {
		return nil, err
	}

	// ensure that the commands are valid
	for _, n := range ast.Children {
		if !validCommitCommands[n.Value] {
			return nil, fmt.Errorf("%s is not a valid change command", n.Value)
		}
	}

	b.runConfig = config
	b.Stdout = ioutil.Discard
	b.Stderr = ioutil.Discard
	b.disableCommit = true

	total := len(ast.Children)
	for _, n := range ast.Children {
		if err := b.checkDispatch(n, false); err != nil {
			return nil, err
		}
	}

	for i, n := range ast.Children {
		if err := b.dispatch(i, total, n); err != nil {
			return nil, err
		}
	}

	return b.runConfig, nil
}

type tmpCacheBackend struct {
	root string
}

func (tcb *tmpCacheBackend) Get(id string) (string, error) {
	d := filepath.Join(tcb.root, id)
	if err := os.MkdirAll(d, 0700); err != nil {
		return "", perrors.Wrapf(err, "failed to create tmp dir for %s", d)
	}
	return d, nil
}
func (tcb *tmpCacheBackend) Remove(id string) error {
	return os.RemoveAll(filepath.Join(tcb.root, id))
}
