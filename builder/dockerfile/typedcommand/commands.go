package typedcommand

import (
	"errors"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/strslice"
)

type KeyValuePair struct {
	Key   string
	Value string
}

func (kvp *KeyValuePair) String() string {
	return kvp.Key + "=" + kvp.Value
}

type KeyValuePairs []KeyValuePair

type CommandSourceCode struct {
	Code string
}

func (c *CommandSourceCode) SourceCode() string {
	return c.Code
}

// SingleWordExpander is a provider for variable expansion where 1 word => 1 output
type SingleWordExpander func(word string) (string, error)

// MultiWordExpander is a provider for variable expansion where 1 word => n output
type MultiWordExpander func(word string) ([]string, error)

type SupportsSingleWordExpansion interface {
	Expand(expander SingleWordExpander) error
}
type SupportsMultiWordExpansion interface {
	Expand(expander MultiWordExpander) error
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

// WithSourceCode is a marker indicating that a given command
// Has been parsed from source code (wich can be displayed in builder output)
type WithSourceCode interface {
	SourceCode() string
}

type EnvCommand struct {
	CommandSourceCode
	Env KeyValuePairs // kvp slice instead of map to preserve ordering
}

func (c *EnvCommand) Expand(expander SingleWordExpander) error {
	return expandKvpsInPlace(c.Env, expander)
}

type MaintainerCommand struct {
	CommandSourceCode
	Maintainer string
}

type LabelCommand struct {
	CommandSourceCode
	Labels KeyValuePairs // kvp slice instead of map to preserve ordering
}

func (c *LabelCommand) Expand(expander SingleWordExpander) error {
	return expandKvpsInPlace(c.Labels, expander)
}

type AddCommand struct {
	CommandSourceCode
	Srcs []string
	Dest string
}

func (c *AddCommand) Expand(expander SingleWordExpander) error {
	dst, err := expander(c.Dest)
	if err != nil {
		return err
	}
	c.Dest = dst
	return expandSliceInPlace(c.Srcs, expander)
}

type CopyCommand struct {
	CommandSourceCode
	Srcs []string
	Dest string
	From string
}

func (c *CopyCommand) Expand(expander SingleWordExpander) error {
	dst, err := expander(c.Dest)
	if err != nil {
		return err
	}
	c.Dest = dst
	from, err := expander(c.From)
	if err != nil {
		return err
	}
	c.From = from
	return expandSliceInPlace(c.Srcs, expander)
}

type FromCommand struct {
	CommandSourceCode
	BaseName  string
	StageName string
}

type OnbuildCommand struct {
	CommandSourceCode
	Expression string
}

func (c *OnbuildCommand) Expand(expander SingleWordExpander) error {
	p, err := expander(c.Expression)
	if err != nil {
		return err
	}
	c.Expression = p
	return nil
}

type WorkdirCommand struct {
	CommandSourceCode
	Path string
}

func (c *WorkdirCommand) Expand(expander SingleWordExpander) error {
	p, err := expander(c.Path)
	if err != nil {
		return err
	}
	c.Path = p
	return nil
}

type RunCommand struct {
	CommandSourceCode
	Expression   strslice.StrSlice
	PrependShell bool
}

type CmdCommand struct {
	CommandSourceCode
	Cmd          strslice.StrSlice
	PrependShell bool
}

type HealthCheckCommand struct {
	CommandSourceCode
	Health *container.HealthConfig
}

type EntrypointCommand struct {
	CommandSourceCode
	Cmd          strslice.StrSlice
	PrependShell bool
}

type ExposeCommand struct {
	CommandSourceCode
	Ports []string
}

func (c *ExposeCommand) Expand(expander MultiWordExpander) error {
	result := []string{}
	for _, v := range c.Ports {
		ps, err := expander(v)
		if err != nil {
			return err
		}
		result = append(result, ps...)
	}
	c.Ports = result
	return nil
}

type UserCommand struct {
	CommandSourceCode
	User string
}

func (c *UserCommand) Expand(expander SingleWordExpander) error {
	p, err := expander(c.User)
	if err != nil {
		return err
	}
	c.User = p
	return nil
}

type VolumeCommand struct {
	CommandSourceCode
	Volumes []string
}

func (c *VolumeCommand) Expand(expander SingleWordExpander) error {
	return expandSliceInPlace(c.Volumes, expander)
}

type StopSignalCommand struct {
	CommandSourceCode
	Signal string
}

func (c *StopSignalCommand) Expand(expander SingleWordExpander) error {
	p, err := expander(c.Signal)
	if err != nil {
		return err
	}
	c.Signal = p
	return nil
}

type ArgCommand struct {
	CommandSourceCode
	Name  string
	Value *string
}

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

type ShellCommand struct {
	CommandSourceCode
	Shell strslice.StrSlice
}

type ResumeBuildCommand struct {
	BaseConfig *container.Config
}

type BuildableStage struct {
	Name     string
	Commands []interface{}
}

func (s *BuildableStage) AddCommand(cmd interface{}) {
	// todo: validate cmd type
	s.Commands = append(s.Commands, cmd)
}

type BuildableStages []BuildableStage

func (s BuildableStages) IsCurrentStage(name string) bool {
	if len(s) == 0 {
		return false
	}
	return s[len(s)-1].Name == name
}

func (s BuildableStages) CurrentStage() (*BuildableStage, error) {
	if len(s) == 0 {
		return nil, errors.New("No build stage in current context")
	}
	return &s[len(s)-1], nil
}

func (s BuildableStages) HasStage(name string) bool {
	for _, stage := range s {
		if stage.Name == name {
			return true
		}
	}
	return false
}
