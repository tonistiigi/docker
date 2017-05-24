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

// WithSourceCode is a marker indicating that a given command
// Has been parsed from source code (wich can be displayed in builder output)
type WithSourceCode interface {
	SourceCode() string
}

type EnvCommand struct {
	OriginalSource string
	Env            KeyValuePairs // kvp slice instead of map to preserve ordering
}

func (c *EnvCommand) SourceCode() string {
	return c.OriginalSource
}

type MaintainerCommand struct {
	OriginalSource string
	Maintainer     string
}

func (c *MaintainerCommand) SourceCode() string {
	return c.OriginalSource
}

type LabelCommand struct {
	OriginalSource string
	Labels         KeyValuePairs // kvp slice instead of map to preserve ordering
}

func (c *LabelCommand) SourceCode() string {
	return c.OriginalSource
}

type AddCommand struct {
	OriginalSource string
	Srcs           []string
	Dest           string
}

func (c *AddCommand) SourceCode() string {
	return c.OriginalSource
}

type CopyCommand struct {
	OriginalSource string
	Srcs           []string
	Dest           string
	From           string
}

func (c *CopyCommand) SourceCode() string {
	return c.OriginalSource
}

type FromCommand struct {
	OriginalSource string
	BaseName       string
	StageName      string
}

func (c *FromCommand) SourceCode() string {
	return c.OriginalSource
}

type OnbuildCommand struct {
	OriginalSource string
	Expression     string
}

func (c *OnbuildCommand) SourceCode() string {
	return c.OriginalSource
}

type WorkdirCommand struct {
	OriginalSource string
	Path           string
}

func (c *WorkdirCommand) SourceCode() string {
	return c.OriginalSource
}

type RunCommand struct {
	OriginalSource string
	Expression     strslice.StrSlice
	PrependShell   bool
}

func (c *RunCommand) SourceCode() string {
	return c.OriginalSource
}

type CmdCommand struct {
	OriginalSource string
	Cmd            strslice.StrSlice
	PrependShell   bool
}

func (c *CmdCommand) SourceCode() string {
	return c.OriginalSource
}

type HealthCheckCommand struct {
	OriginalSource string
	Health         *container.HealthConfig
}

func (c *HealthCheckCommand) SourceCode() string {
	return c.OriginalSource
}

type EntrypointCommand struct {
	OriginalSource string
	CmdLine        strslice.StrSlice
	Discard        bool
	PrependShell   bool
}

func (c *EntrypointCommand) SourceCode() string {
	return c.OriginalSource
}

type ExposeCommand struct {
	OriginalSource string
	Ports          []string
}

func (c *ExposeCommand) SourceCode() string {
	return c.OriginalSource
}

type UserCommand struct {
	OriginalSource string
	User           string
}

func (c *UserCommand) SourceCode() string {
	return c.OriginalSource
}

type VolumeCommand struct {
	OriginalSource string
	Volumes        []string
}

func (c *VolumeCommand) SourceCode() string {
	return c.OriginalSource
}

type StopSignalCommand struct {
	OriginalSource string
	Sig            string
}

func (c *StopSignalCommand) SourceCode() string {
	return c.OriginalSource
}

type ArgCommand struct {
	OriginalSource string
	Arg            string
}

func (c *ArgCommand) SourceCode() string {
	return c.OriginalSource
}

type ShellCommand struct {
	OriginalSource string
	Shell          strslice.StrSlice
}

func (c *ShellCommand) SourceCode() string {
	return c.OriginalSource
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
