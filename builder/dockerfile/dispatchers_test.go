package dockerfile

import (
	"bytes"
	"context"
	"runtime"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/backend"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/strslice"
	"github.com/docker/docker/builder"
	"github.com/docker/docker/builder/dockerfile/instructions"
	"github.com/docker/docker/pkg/system"
	"github.com/docker/go-connections/nat"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newBuilderWithMockBackend() *Builder {
	mockBackend := &MockBackend{}
	ctx := context.Background()
	b := &Builder{
		options:       &types.ImageBuildOptions{},
		docker:        mockBackend,
		buildArgs:     newBuildArgs(make(map[string]*string)),
		Stdout:        new(bytes.Buffer),
		clientCtx:     ctx,
		disableCommit: true,
		imageSources: newImageSources(ctx, builderOptions{
			Options: &types.ImageBuildOptions{},
			Backend: mockBackend,
		}),
		buildStages:      newBuildStages(),
		imageProber:      newImageProber(mockBackend, nil, runtime.GOOS, false),
		containerManager: newContainerManager(mockBackend),
	}
	return b
}

func TestEnv2Variables(t *testing.T) {
	b := newBuilderWithMockBackend()
	sb := newStageBuilder(b, '\\', nil)
	envCommand := &instructions.EnvCommand{
		Env: instructions.KeyValuePairs{
			instructions.KeyValuePair{Key: "var1", Value: "val1"},
			instructions.KeyValuePair{Key: "var2", Value: "val2"},
		},
	}
	err := sb.dispatch(envCommand)
	require.NoError(t, err)

	expected := []string{
		"var1=val1",
		"var2=val2",
	}
	assert.Equal(t, expected, sb.state.runConfig.Env)
}

func TestEnvValueWithExistingRunConfigEnv(t *testing.T) {
	b := newBuilderWithMockBackend()
	sb := newStageBuilder(b, '\\', nil)
	sb.state.runConfig.Env = []string{"var1=old", "var2=fromenv"}
	envCommand := &instructions.EnvCommand{
		Env: instructions.KeyValuePairs{
			instructions.KeyValuePair{Key: "var1", Value: "val1"},
		},
	}
	err := sb.dispatch(envCommand)
	require.NoError(t, err)
	expected := []string{
		"var1=val1",
		"var2=fromenv",
	}
	assert.Equal(t, expected, sb.state.runConfig.Env)
}

func TestMaintainer(t *testing.T) {
	maintainerEntry := "Some Maintainer <maintainer@example.com>"
	b := newBuilderWithMockBackend()
	sb := newStageBuilder(b, '\\', nil)
	cmd := &instructions.MaintainerCommand{Maintainer: maintainerEntry}
	err := sb.dispatch(cmd)
	require.NoError(t, err)
	assert.Equal(t, maintainerEntry, sb.state.maintainer)
}

func TestLabel(t *testing.T) {
	labelName := "label"
	labelValue := "value"

	b := newBuilderWithMockBackend()
	sb := newStageBuilder(b, '\\', nil)
	cmd := &instructions.LabelCommand{
		Labels: instructions.KeyValuePairs{
			instructions.KeyValuePair{Key: labelName, Value: labelValue},
		},
	}
	err := sb.dispatch(cmd)
	require.NoError(t, err)

	require.Contains(t, sb.state.runConfig.Labels, labelName)
	assert.Equal(t, sb.state.runConfig.Labels[labelName], labelValue)
}

func TestFromScratch(t *testing.T) {
	b := newBuilderWithMockBackend()
	sb := newStageBuilder(b, '\\', nil)
	cmd := &instructions.FromCommand{
		BaseName: "scratch",
	}
	err := sb.dispatch(cmd)

	if runtime.GOOS == "windows" && !system.LCOWSupported() {
		assert.EqualError(t, err, "Windows does not support FROM scratch")
		return
	}

	require.NoError(t, err)
	assert.True(t, sb.state.hasFromImage())
	assert.Equal(t, "", sb.state.imageID)
	// Windows does not set the default path. TODO @jhowardmsft LCOW support. This will need revisiting as we get further into the implementation
	expected := "PATH=" + system.DefaultPathEnv(runtime.GOOS)
	if runtime.GOOS == "windows" {
		expected = ""
	}
	assert.Equal(t, []string{expected}, sb.state.runConfig.Env)
}

func TestFromWithArg(t *testing.T) {
	tag, expected := ":sometag", "expectedthisid"

	getImage := func(name string) (builder.Image, builder.ReleaseableLayer, error) {
		assert.Equal(t, "alpine"+tag, name)
		return &mockImage{id: "expectedthisid"}, nil, nil
	}
	b := newBuilderWithMockBackend()
	b.docker.(*MockBackend).getImageFunc = getImage
	sb := newStageBuilder(b, '\\', nil)

	val := "sometag"
	metaArg := instructions.ArgCommand{
		Name:  "THETAG",
		Value: &val,
	}
	cmd := &instructions.FromCommand{
		BaseName: "alpine:${THETAG}",
	}
	err := b.processMetaArg(metaArg, NewShellLex('\\'))
	require.NoError(t, err)
	err = sb.dispatch(cmd)
	require.NoError(t, err)

	assert.Equal(t, expected, sb.state.imageID)
	assert.Equal(t, expected, sb.state.baseImage.ImageID())
	assert.Len(t, b.buildArgs.GetAllAllowed(), 0)
	assert.Len(t, b.buildArgs.GetAllMeta(), 1)
}

func TestFromWithUndefinedArg(t *testing.T) {
	tag, expected := "sometag", "expectedthisid"

	getImage := func(name string) (builder.Image, builder.ReleaseableLayer, error) {
		assert.Equal(t, "alpine", name)
		return &mockImage{id: "expectedthisid"}, nil, nil
	}
	b := newBuilderWithMockBackend()
	b.docker.(*MockBackend).getImageFunc = getImage
	sb := newStageBuilder(b, '\\', nil)

	b.options.BuildArgs = map[string]*string{"THETAG": &tag}

	cmd := &instructions.FromCommand{
		BaseName: "alpine${THETAG}",
	}
	err := sb.dispatch(cmd)
	require.NoError(t, err)
	assert.Equal(t, expected, sb.state.imageID)
}

func TestFromMultiStageWithNamedStage(t *testing.T) {
	b := newBuilderWithMockBackend()
	firstFrom := &instructions.FromCommand{BaseName: "someimg", StageName: "base"}
	secondFrom := &instructions.FromCommand{BaseName: "base"}
	firstSB := newStageBuilder(b, '\\', nil)
	secondSB := newStageBuilder(b, '\\', nil)
	err := firstSB.dispatch(firstFrom)
	require.NoError(t, err)
	assert.True(t, firstSB.state.hasFromImage())
	err = secondSB.dispatch(secondFrom)
	require.NoError(t, err)
	assert.True(t, secondSB.state.hasFromImage())
}

func TestOnbuild(t *testing.T) {
	b := newBuilderWithMockBackend()
	sb := newStageBuilder(b, '\\', nil)
	cmd := &instructions.OnbuildCommand{
		Expression: "ADD . /app/src",
	}
	err := sb.dispatch(cmd)
	require.NoError(t, err)
	assert.Equal(t, "ADD . /app/src", sb.state.runConfig.OnBuild[0])
}

func TestWorkdir(t *testing.T) {
	b := newBuilderWithMockBackend()
	sb := newStageBuilder(b, '`', nil)
	workingDir := "/app"
	if runtime.GOOS == "windows" {
		workingDir = "C:\\app"
	}
	cmd := &instructions.WorkdirCommand{
		Path: workingDir,
	}

	err := sb.dispatch(cmd)
	require.NoError(t, err)
	assert.Equal(t, workingDir, sb.state.runConfig.WorkingDir)
}

func TestCmd(t *testing.T) {
	b := newBuilderWithMockBackend()
	sb := newStageBuilder(b, '`', nil)
	command := "./executable"

	cmd := &instructions.CmdCommand{
		Cmd:          strslice.StrSlice{command},
		PrependShell: true,
	}
	err := sb.dispatch(cmd)
	require.NoError(t, err)

	var expectedCommand strslice.StrSlice
	if runtime.GOOS == "windows" {
		expectedCommand = strslice.StrSlice(append([]string{"cmd"}, "/S", "/C", command))
	} else {
		expectedCommand = strslice.StrSlice(append([]string{"/bin/sh"}, "-c", command))
	}

	assert.Equal(t, expectedCommand, sb.state.runConfig.Cmd)
	assert.True(t, sb.state.cmdSet)
}

func TestHealthcheckNone(t *testing.T) {
	b := newBuilderWithMockBackend()
	sb := newStageBuilder(b, '`', nil)
	cmd := &instructions.HealthCheckCommand{
		Health: &container.HealthConfig{
			Test: []string{"NONE"},
		},
	}
	err := sb.dispatch(cmd)
	require.NoError(t, err)

	require.NotNil(t, sb.state.runConfig.Healthcheck)
	assert.Equal(t, []string{"NONE"}, sb.state.runConfig.Healthcheck.Test)
}

func TestHealthcheckCmd(t *testing.T) {

	b := newBuilderWithMockBackend()
	sb := newStageBuilder(b, '`', nil)
	expectedTest := []string{"CMD-SHELL", "curl -f http://localhost/ || exit 1"}
	cmd := &instructions.HealthCheckCommand{
		Health: &container.HealthConfig{
			Test: expectedTest,
		},
	}
	err := sb.dispatch(cmd)
	require.NoError(t, err)

	require.NotNil(t, sb.state.runConfig.Healthcheck)
	assert.Equal(t, expectedTest, sb.state.runConfig.Healthcheck.Test)
}

func TestEntrypoint(t *testing.T) {
	b := newBuilderWithMockBackend()
	sb := newStageBuilder(b, '`', nil)
	entrypointCmd := "/usr/sbin/nginx"

	cmd := &instructions.EntrypointCommand{Cmd: strslice.StrSlice{entrypointCmd}, PrependShell: true}
	err := sb.dispatch(cmd)
	require.NoError(t, err)
	require.NotNil(t, sb.state.runConfig.Entrypoint)

	var expectedEntrypoint strslice.StrSlice
	if runtime.GOOS == "windows" {
		expectedEntrypoint = strslice.StrSlice(append([]string{"cmd"}, "/S", "/C", entrypointCmd))
	} else {
		expectedEntrypoint = strslice.StrSlice(append([]string{"/bin/sh"}, "-c", entrypointCmd))
	}
	assert.Equal(t, expectedEntrypoint, sb.state.runConfig.Entrypoint)
}

func TestExpose(t *testing.T) {
	b := newBuilderWithMockBackend()
	sb := newStageBuilder(b, '`', nil)

	exposedPort := "80"
	cmd := &instructions.ExposeCommand{
		Ports: []string{exposedPort},
	}
	err := sb.dispatch(cmd)
	require.NoError(t, err)

	require.NotNil(t, sb.state.runConfig.ExposedPorts)
	require.Len(t, sb.state.runConfig.ExposedPorts, 1)

	portsMapping, err := nat.ParsePortSpec(exposedPort)
	require.NoError(t, err)
	assert.Contains(t, sb.state.runConfig.ExposedPorts, portsMapping[0].Port)
}

func TestUser(t *testing.T) {
	b := newBuilderWithMockBackend()
	sb := newStageBuilder(b, '`', nil)

	cmd := &instructions.UserCommand{
		User: "test",
	}
	err := sb.dispatch(cmd)
	require.NoError(t, err)
	assert.Equal(t, "test", sb.state.runConfig.User)
}

func TestVolume(t *testing.T) {
	b := newBuilderWithMockBackend()
	sb := newStageBuilder(b, '`', nil)

	exposedVolume := "/foo"

	cmd := &instructions.VolumeCommand{
		Volumes: []string{exposedVolume},
	}
	err := sb.dispatch(cmd)
	require.NoError(t, err)
	require.NotNil(t, sb.state.runConfig.Volumes)
	assert.Len(t, sb.state.runConfig.Volumes, 1)
	assert.Contains(t, sb.state.runConfig.Volumes, exposedVolume)
}

func TestStopSignal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not support stopsignal")
		return
	}
	b := newBuilderWithMockBackend()
	sb := newStageBuilder(b, '`', nil)
	signal := "SIGKILL"

	cmd := &instructions.StopSignalCommand{
		Signal: signal,
	}
	err := sb.dispatch(cmd)
	require.NoError(t, err)
	assert.Equal(t, signal, sb.state.runConfig.StopSignal)
}

func TestArg(t *testing.T) {
	b := newBuilderWithMockBackend()
	sb := newStageBuilder(b, '`', nil)

	argName := "foo"
	argVal := "bar"
	cmd := &instructions.ArgCommand{Name: argName, Value: &argVal}
	err := sb.dispatch(cmd)
	require.NoError(t, err)

	expected := map[string]string{argName: argVal}
	assert.Equal(t, expected, b.buildArgs.GetAllAllowed())
}

func TestShell(t *testing.T) {
	b := newBuilderWithMockBackend()
	sb := newStageBuilder(b, '`', nil)

	shellCmd := "powershell"
	cmd := &instructions.ShellCommand{Shell: strslice.StrSlice{shellCmd}}

	err := sb.dispatch(cmd)
	require.NoError(t, err)

	expectedShell := strslice.StrSlice([]string{shellCmd})
	assert.Equal(t, expectedShell, sb.state.runConfig.Shell)
}

func TestPrependEnvOnCmd(t *testing.T) {
	buildArgs := newBuildArgs(nil)
	buildArgs.AddArg("NO_PROXY", nil)

	args := []string{"sorted=nope", "args=not", "http_proxy=foo", "NO_PROXY=YA"}
	cmd := []string{"foo", "bar"}
	cmdWithEnv := prependEnvOnCmd(buildArgs, args, cmd)
	expected := strslice.StrSlice([]string{
		"|3", "NO_PROXY=YA", "args=not", "sorted=nope", "foo", "bar"})
	assert.Equal(t, expected, cmdWithEnv)
}

func TestRunWithBuildArgs(t *testing.T) {
	b := newBuilderWithMockBackend()
	b.buildArgs.argsFromOptions["HTTP_PROXY"] = strPtr("FOO")
	b.disableCommit = false
	sb := newStageBuilder(b, '`', nil)

	runConfig := &container.Config{}
	origCmd := strslice.StrSlice([]string{"cmd", "in", "from", "image"})
	cmdWithShell := strslice.StrSlice(append(getShell(runConfig, runtime.GOOS), "echo foo"))
	envVars := []string{"|1", "one=two"}
	cachedCmd := strslice.StrSlice(append(envVars, cmdWithShell...))

	imageCache := &mockImageCache{
		getCacheFunc: func(parentID string, cfg *container.Config) (string, error) {
			// Check the runConfig.Cmd sent to probeCache()
			assert.Equal(t, cachedCmd, cfg.Cmd)
			assert.Equal(t, strslice.StrSlice(nil), cfg.Entrypoint)
			return "", nil
		},
	}

	mockBackend := b.docker.(*MockBackend)
	mockBackend.makeImageCacheFunc = func(_ []string, _ string) builder.ImageCache {
		return imageCache
	}
	b.imageProber = newImageProber(mockBackend, nil, runtime.GOOS, false)
	mockBackend.getImageFunc = func(_ string) (builder.Image, builder.ReleaseableLayer, error) {
		return &mockImage{
			id:     "abcdef",
			config: &container.Config{Cmd: origCmd},
		}, nil, nil
	}
	mockBackend.containerCreateFunc = func(config types.ContainerCreateConfig) (container.ContainerCreateCreatedBody, error) {
		// Check the runConfig.Cmd sent to create()
		assert.Equal(t, cmdWithShell, config.Config.Cmd)
		assert.Contains(t, config.Config.Env, "one=two")
		assert.Equal(t, strslice.StrSlice{""}, config.Config.Entrypoint)
		return container.ContainerCreateCreatedBody{ID: "12345"}, nil
	}
	mockBackend.commitFunc = func(cID string, cfg *backend.ContainerCommitConfig) (string, error) {
		// Check the runConfig.Cmd sent to commit()
		assert.Equal(t, origCmd, cfg.Config.Cmd)
		assert.Equal(t, cachedCmd, cfg.ContainerConfig.Cmd)
		assert.Equal(t, strslice.StrSlice(nil), cfg.Config.Entrypoint)
		return "", nil
	}
	from := &instructions.FromCommand{BaseName: "abcdef"}
	err := sb.dispatch(from)
	require.NoError(t, err)
	b.buildArgs.AddArg("one", strPtr("two"))
	run := &instructions.RunCommand{Expression: strslice.StrSlice{"echo foo"}, PrependShell: true}
	require.NoError(t, sb.dispatch(run))

	// Check that runConfig.Cmd has not been modified by run
	assert.Equal(t, origCmd, sb.state.runConfig.Cmd)
}
