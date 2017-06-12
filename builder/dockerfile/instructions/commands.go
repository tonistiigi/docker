package instructions

import (
	"errors"

	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/strslice"
)

// KeyValuePair represent an arbitrary named value (usefull in slice insted of map[string] string to preserve ordering)
type KeyValuePair struct {
	Key   string
	Value string
}

func (kvp *KeyValuePair) String() string {
	return kvp.Key + "=" + kvp.Value
}

// KeyValuePairs is a slice of KeyValuePair
type KeyValuePairs []KeyValuePair

// WithNameAndCode is the base class for Dockerfile (String() returns its source code)
type WithNameAndCode struct {
	code string
	name string
}

func (c *WithNameAndCode) String() string {
	return c.code
}

// Name of the command
func (c *WithNameAndCode) Name() string {
	return c.name
}

func newWithNameAndCode(req parseRequest) WithNameAndCode {
	return WithNameAndCode{code: strings.TrimSpace(req.original), name: req.command}
}

// SingleWordExpander is a provider for variable expansion where 1 word => 1 output
type SingleWordExpander func(word string) (string, error)

// SupportsSingleWordExpansion interface marks a command as supporting variable expansion
type SupportsSingleWordExpansion interface {
	Expand(expander SingleWordExpander) error
}

func expandKvp(kvp KeyValuePair, expander SingleWordExpander) (KeyValuePair, error) {
	key, err := expander(kvp.Key)
	if err != nil {
		return KeyValuePair{}, err
	}
	value, err := expander(kvp.Value)
	if err != nil {
		return KeyValuePair{}, err
	}
	return KeyValuePair{Key: key, Value: value}, nil
}
func expandKvpsInPlace(kvps KeyValuePairs, expander SingleWordExpander) error {
	for i, kvp := range kvps {
		newKvp, err := expandKvp(kvp, expander)
		if err != nil {
			return err
		}
		kvps[i] = newKvp
	}
	return nil
}

func expandSliceInPlace(values []string, expander SingleWordExpander) error {
	for i, v := range values {
		newValue, err := expander(v)
		if err != nil {
			return err
		}
		values[i] = newValue
	}
	return nil
}

// EnvCommand : ENV key1 value1 [keyN valueN...]
type EnvCommand struct {
	WithNameAndCode
	Env KeyValuePairs // kvp slice instead of map to preserve ordering
}

// Expand variables
func (c *EnvCommand) Expand(expander SingleWordExpander) error {
	return expandKvpsInPlace(c.Env, expander)
}

// MaintainerCommand : MAINTAINER maintainer_name
type MaintainerCommand struct {
	WithNameAndCode
	Maintainer string
}

// LabelCommand : LABEL some json data describing the image
//
// Sets the Label variable foo to bar,
//
type LabelCommand struct {
	WithNameAndCode
	Labels KeyValuePairs // kvp slice instead of map to preserve ordering
}

// Expand variables
func (c *LabelCommand) Expand(expander SingleWordExpander) error {
	return expandKvpsInPlace(c.Labels, expander)
}

// AddCommand : ADD foo /path
//
// Add the file 'foo' to '/path'. Tarball and Remote URL (git, http) handling
// exist here. If you do not wish to have this automatic handling, use COPY.
//
type AddCommand struct {
	WithNameAndCode
	Srcs  []string
	Dest  string
	Chown string
}

// Expand variables
func (c *AddCommand) Expand(expander SingleWordExpander) error {
	dst, err := expander(c.Dest)
	if err != nil {
		return err
	}
	c.Dest = dst
	return expandSliceInPlace(c.Srcs, expander)
}

// CopyCommand : COPY foo /path
//
// Same as 'ADD' but without the tar and remote url handling.
//
type CopyCommand struct {
	WithNameAndCode
	Srcs  []string
	Dest  string
	From  string
	Chown string
}

// Expand variables
func (c *CopyCommand) Expand(expander SingleWordExpander) error {
	dst, err := expander(c.Dest)
	if err != nil {
		return err
	}
	c.Dest = dst
	return expandSliceInPlace(c.Srcs, expander)
}

// FromCommand : FROM imagename[:tag | @digest] [AS build-stage-name]
//
type FromCommand struct {
	WithNameAndCode
	BaseName  string
	StageName string
}

// OnbuildCommand : ONBUILD <some other command>
type OnbuildCommand struct {
	WithNameAndCode
	Expression string
}

// WorkdirCommand : WORKDIR /tmp
//
// Set the working directory for future RUN/CMD/etc statements.
//
type WorkdirCommand struct {
	WithNameAndCode
	Path string
}

// Expand variables
func (c *WorkdirCommand) Expand(expander SingleWordExpander) error {
	p, err := expander(c.Path)
	if err != nil {
		return err
	}
	c.Path = p
	return nil
}

// RunCommand : RUN some command yo
//
// run a command and commit the image. Args are automatically prepended with
// the current SHELL which defaults to 'sh -c' under linux or 'cmd /S /C' under
// Windows, in the event there is only one argument The difference in processing:
//
// RUN echo hi          # sh -c echo hi       (Linux)
// RUN echo hi          # cmd /S /C echo hi   (Windows)
// RUN [ "echo", "hi" ] # echo hi
//
type RunCommand struct {
	WithNameAndCode
	Expression   strslice.StrSlice
	PrependShell bool
}

// CmdCommand : CMD foo
//
// Set the default command to run in the container (which may be empty).
// Argument handling is the same as RUN.
//
type CmdCommand struct {
	WithNameAndCode
	Cmd          strslice.StrSlice
	PrependShell bool
}

// HealthCheckCommand : HEALTHCHECK foo
//
// Set the default healthcheck command to run in the container (which may be empty).
// Argument handling is the same as RUN.
//
type HealthCheckCommand struct {
	WithNameAndCode
	Health *container.HealthConfig
}

// EntrypointCommand : ENTRYPOINT /usr/sbin/nginx
//
// Set the entrypoint to /usr/sbin/nginx. Will accept the CMD as the arguments
// to /usr/sbin/nginx. Uses the default shell if not in JSON format.
//
// Handles command processing similar to CMD and RUN, only req.runConfig.Entrypoint
// is initialized at newBuilder time instead of through argument parsing.
//
type EntrypointCommand struct {
	WithNameAndCode
	Cmd          strslice.StrSlice
	PrependShell bool
}

// ExposeCommand : EXPOSE 6667/tcp 7000/tcp
//
// Expose ports for links and port mappings. This all ends up in
// req.runConfig.ExposedPorts for runconfig.
//
type ExposeCommand struct {
	WithNameAndCode
	Ports []string
}

// UserCommand : USER foo
//
// Set the user to 'foo' for future commands and when running the
// ENTRYPOINT/CMD at container run time.
//
type UserCommand struct {
	WithNameAndCode
	User string
}

// Expand variables
func (c *UserCommand) Expand(expander SingleWordExpander) error {
	p, err := expander(c.User)
	if err != nil {
		return err
	}
	c.User = p
	return nil
}

// VolumeCommand : VOLUME /foo
//
// Expose the volume /foo for use. Will also accept the JSON array form.
//
type VolumeCommand struct {
	WithNameAndCode
	Volumes []string
}

// Expand variables
func (c *VolumeCommand) Expand(expander SingleWordExpander) error {
	return expandSliceInPlace(c.Volumes, expander)
}

// StopSignalCommand : STOPSIGNAL signal
//
// Set the signal that will be used to kill the container.
type StopSignalCommand struct {
	WithNameAndCode
	Signal string
}

// Expand variables
func (c *StopSignalCommand) Expand(expander SingleWordExpander) error {
	p, err := expander(c.Signal)
	if err != nil {
		return err
	}
	c.Signal = p
	return nil
}

// ArgCommand : ARG name[=value]
//
// Adds the variable foo to the trusted list of variables that can be passed
// to builder using the --build-arg flag for expansion/substitution or passing to 'run'.
// Dockerfile author may optionally set a default value of this variable.
type ArgCommand struct {
	WithNameAndCode
	Name  string
	Value *string
}

// Expand variables
func (c *ArgCommand) Expand(expander SingleWordExpander) error {
	p, err := expander(c.Name)
	if err != nil {
		return err
	}
	c.Name = p
	if c.Value != nil {
		p, err = expander(*c.Value)
		if err != nil {
			return err
		}
		c.Value = &p
	}
	return nil
}

// ShellCommand : SHELL powershell -command
//
// Set the non-default shell to use.
type ShellCommand struct {
	WithNameAndCode
	Shell strslice.StrSlice
}

// ResumeBuildCommand allows to resume a build operation from a given container config
type ResumeBuildCommand struct {
	BaseConfig *container.Config
}

// BuildableStage represents a single stage in a multi-stage build
type BuildableStage struct {
	Name     string
	Commands []interface{}
}

// AddCommand to the stage
func (s *BuildableStage) AddCommand(cmd interface{}) {
	// todo: validate cmd type
	s.Commands = append(s.Commands, cmd)
}

// IsCurrentStage check if the stage name is the current stage
func IsCurrentStage(s []BuildableStage, name string) bool {
	if len(s) == 0 {
		return false
	}
	return s[len(s)-1].Name == name
}

// CurrentStage return the last stage in a slice
func CurrentStage(s []BuildableStage) (*BuildableStage, error) {
	if len(s) == 0 {
		return nil, errors.New("No build stage in current context")
	}
	return &s[len(s)-1], nil
}

// HasStage looks for the presence of a given stage name
func HasStage(s []BuildableStage, name string) (bool, int) {
	for i, stage := range s {
		if stage.Name == name {
			return true, i
		}
	}
	return false, -1
}
