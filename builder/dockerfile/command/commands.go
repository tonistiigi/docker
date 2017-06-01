package command

import (
	"errors"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/strslice"
)

type KeyValuePair struct {
	Key   string
	Value string
}

type KeyValuePairs []KeyValuePair

type CommandSourceCode struct {
	Code string
}

func (c *CommandSourceCode) SourceCode() string {
	return c.Code
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

type MaintainerCommand struct {
	CommandSourceCode
	Maintainer string
}

type LabelCommand struct {
	CommandSourceCode
	Labels KeyValuePairs // kvp slice instead of map to preserve ordering
}

type AddCommand struct {
	CommandSourceCode
	Srcs []string
	Dest string
}

type CopyCommand struct {
	CommandSourceCode
	Srcs []string
	Dest string
	From string
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

type WorkdirCommand struct {
	CommandSourceCode
	Path string
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
	CmdLine      strslice.StrSlice
	Discard      bool
	PrependShell bool
}

type ExposeCommand struct {
	CommandSourceCode
	Ports []string
}

type UserCommand struct {
	CommandSourceCode
	User string
}

type VolumeCommand struct {
	CommandSourceCode
	Volumes []string
}

type StopSignalCommand struct {
	CommandSourceCode
	Sig string
}

type ArgCommand struct {
	CommandSourceCode
	Arg string
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

type ParsingResult struct {
	Stages []BuildableStage
}

func (s *ParsingResult) IsCurrentStage(name string) bool {
	if len(s.Stages) == 0 {
		return false
	}
	return s.Stages[len(s.Stages)-1].Name == name
}

func (s *ParsingResult) CurrentStage() (*BuildableStage, error) {
	if len(s.Stages) == 0 {
		return nil, errors.New("No build stage in current context")
	}
	return &s.Stages[len(s.Stages)-1], nil
}

func (s *ParsingResult) HasStage(name string) bool {
	for _, stage := range s.Stages {
		if stage.Name == name {
			return true
		}
	}
	return false
}
