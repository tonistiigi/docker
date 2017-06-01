package typedcommand

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/strslice"
	"github.com/docker/docker/builder/dockerfile/command"
	"github.com/docker/docker/builder/dockerfile/parser"
	"github.com/docker/go-connections/nat"
	metrics "github.com/docker/go-metrics"
	"github.com/pkg/errors"
)

const (
	metricsUnknownInstructionError = "unknown_instruction_error"
)

type parseRequest struct {
	args       []string
	attributes map[string]bool
	flags      *BFlags
	original   string
}

func nodeArgs(node *parser.Node) []string {
	result := []string{}
	for ; node.Next != nil; node = node.Next {
		arg := node.Next
		result = append(result, arg.Value)
	}
	return result
}

func newParseRequestFromNode(node *parser.Node) parseRequest {
	return parseRequest{
		args:       nodeArgs(node),
		attributes: node.Attributes,
		original:   node.Original,
		flags:      NewBFlagsWithArgs(node.Flags),
	}
}

func ParseCommand(node *parser.Node, buildsFailed metrics.LabeledCounter) (interface{}, error) {
	switch node.Value {
	case command.Env:
		return parseEnv(newParseRequestFromNode(node))
	case command.Maintainer:
		return parseMaintainer(newParseRequestFromNode(node))
	case command.Label:
		return parseLabel(newParseRequestFromNode(node))
	case command.Add:
		return parseAdd(newParseRequestFromNode(node))
	case command.Copy:
		return parseCopy(newParseRequestFromNode(node))
	case command.From:
		return parseFrom(newParseRequestFromNode(node))
	case command.Onbuild:
		return parseOnbuild(newParseRequestFromNode(node))
	case command.Workdir:
		return parseWorkdir(newParseRequestFromNode(node))
	case command.Run:
		return parseRun(newParseRequestFromNode(node))
	case command.Cmd:
		return parseCmd(newParseRequestFromNode(node))
	case command.Healthcheck:
		return parseHealthcheck(newParseRequestFromNode(node))
	case command.Entrypoint:
		return parseEntrypoint(newParseRequestFromNode(node))
	case command.Expose:
		return parseExpose(newParseRequestFromNode(node))
	case command.User:
		return parseUser(newParseRequestFromNode(node))
	case command.Volume:
		return parseVolume(newParseRequestFromNode(node))
	case command.StopSignal:
		return parseStopSignal(newParseRequestFromNode(node))
	case command.Arg:
		return parseArg(newParseRequestFromNode(node))
	case command.Shell:
		return parseShell(newParseRequestFromNode(node))
	}

	buildsFailed.WithValues(metricsUnknownInstructionError).Inc()
	return nil, fmt.Errorf("unknown instruction: %s", strings.ToUpper(node.Value))
}

func Parse(ast *parser.Node, targetStage string, buildsFailed metrics.LabeledCounter) (stages BuildableStages, metaArgs []ArgCommand, err error) {
	for _, n := range ast.Children {
		if n.Value == command.From && stages.IsCurrentStage(targetStage) {
			break
		}
		cmd, err := ParseCommand(n, buildsFailed)
		if err != nil {
			return nil, nil, err
		}
		switch c := cmd.(type) {
		case *ArgCommand:
			if len(stages) == 0 {
				metaArgs = append(metaArgs, *c)
			} else {
				stage, _ := stages.CurrentStage()
				stage.AddCommand(cmd)
			}
		case *FromCommand:
			if c.StageName == "" {
				c.StageName = strconv.Itoa(len(stages))
			}
			stage := BuildableStage{Name: c.StageName, Commands: []interface{}{c}}
			stages = append(stages, stage)
		default:
			stage, err := stages.CurrentStage()
			if err != nil {
				return nil, nil, err
			}
			stage.AddCommand(cmd)
		}

	}
	return stages, metaArgs, nil
}

func parseEnv(req parseRequest) (*EnvCommand, error) {
	// this one has side effect on parsing. So it is evaluated early
	if len(req.args) == 0 {
		return nil, errAtLeastOneArgument("ENV")
	}

	if len(req.args)%2 != 0 {
		// should never get here, but just in case
		return nil, errTooManyArguments("ENV")
	}

	if err := req.flags.Parse(); err != nil {
		return nil, err
	}
	var envs KeyValuePairs
	for j := 0; j < len(req.args); j += 2 {
		if len(req.args[j]) == 0 {
			return nil, errBlankCommandNames("ENV")
		}
		name := req.args[j]
		value := req.args[j+1]
		envs = append(envs, KeyValuePair{Key: name, Value: value})
	}
	return &EnvCommand{
		Env:               envs,
		CommandSourceCode: CommandSourceCode{req.original},
	}, nil
}

func parseMaintainer(req parseRequest) (*MaintainerCommand, error) {
	if len(req.args) != 1 {
		return nil, errExactlyOneArgument("MAINTAINER")
	}

	if err := req.flags.Parse(); err != nil {
		return nil, err
	}
	return &MaintainerCommand{
		Maintainer:        req.args[0],
		CommandSourceCode: CommandSourceCode{req.original},
	}, nil
}

func parseLabel(req parseRequest) (*LabelCommand, error) {
	if len(req.args) == 0 {
		return nil, errAtLeastOneArgument("LABEL")
	}
	if len(req.args)%2 != 0 {
		// should never get here, but just in case
		return nil, errTooManyArguments("LABEL")
	}

	if err := req.flags.Parse(); err != nil {
		return nil, err
	}

	labels := KeyValuePairs{}

	for j := 0; j < len(req.args); j++ {
		name := req.args[j]
		if name == "" {
			return nil, errBlankCommandNames("LABEL")
		}

		value := req.args[j+1]

		labels = append(labels, KeyValuePair{Key: name, Value: value})
		j++
	}
	return &LabelCommand{
		Labels:            labels,
		CommandSourceCode: CommandSourceCode{req.original},
	}, nil
}

func parseAdd(req parseRequest) (*AddCommand, error) {
	if len(req.args) < 2 {
		return nil, errAtLeastTwoArguments("ADD")
	}

	if err := req.flags.Parse(); err != nil {
		return nil, err
	}

	return &AddCommand{
		Srcs:              req.args[:len(req.args)-1],
		Dest:              req.args[len(req.args)-1],
		CommandSourceCode: CommandSourceCode{req.original},
	}, nil
}

func parseCopy(req parseRequest) (*CopyCommand, error) {
	if len(req.args) < 2 {
		return nil, errAtLeastTwoArguments("COPY")
	}

	flFrom := req.flags.AddString("from", "")
	if err := req.flags.Parse(); err != nil {
		return nil, err
	}
	return &CopyCommand{
		Srcs:              req.args[:len(req.args)-1],
		Dest:              req.args[len(req.args)-1],
		From:              flFrom.Value,
		CommandSourceCode: CommandSourceCode{req.original},
	}, nil
}

func parseFrom(req parseRequest) (*FromCommand, error) {
	// HELP HERE <- do we need to validate stageName at dispatch time (do we authorize env / buildArgs in here?)
	stageName, err := parseBuildStageName(req.args)
	if err != nil {
		return nil, err
	}

	if err := req.flags.Parse(); err != nil {
		return nil, err
	}

	return &FromCommand{
		BaseName:          req.args[0],
		StageName:         stageName,
		CommandSourceCode: CommandSourceCode{req.original},
	}, nil

	//TODO
	//req.buildArgs.ResetAllowed()

}

func parseBuildStageName(args []string) (string, error) {
	stageName := ""
	switch {
	case len(args) == 3 && strings.EqualFold(args[1], "as"):
		stageName = strings.ToLower(args[2])
		if ok, _ := regexp.MatchString("^[a-z][a-z0-9-_\\.]*$", stageName); !ok {
			return "", errors.Errorf("invalid name for build stage: %q, name can't start with a number or contain symbols", stageName)
		}
	case len(args) != 1:
		return "", errors.New("FROM requires either one or three arguments")
	}

	return stageName, nil
}

func parseOnbuild(req parseRequest) (*OnbuildCommand, error) {
	if len(req.args) == 0 {
		return nil, errAtLeastOneArgument("ONBUILD")
	}
	if err := req.flags.Parse(); err != nil {
		return nil, err
	}

	triggerInstruction := strings.ToUpper(strings.TrimSpace(req.args[0]))
	switch triggerInstruction {
	case "ONBUILD":
		return nil, errors.New("Chaining ONBUILD via `ONBUILD ONBUILD` isn't allowed")
	case "MAINTAINER", "FROM":
		return nil, fmt.Errorf("%s isn't allowed as an ONBUILD trigger", triggerInstruction)
	}

	original := regexp.MustCompile(`(?i)^\s*ONBUILD\s*`).ReplaceAllString(req.original, "")
	return &OnbuildCommand{
		Expression:        original,
		CommandSourceCode: CommandSourceCode{req.original},
	}, nil

}

func parseWorkdir(req parseRequest) (*WorkdirCommand, error) {
	if len(req.args) != 1 {
		return nil, errExactlyOneArgument("WORKDIR")
	}

	err := req.flags.Parse()
	if err != nil {
		return nil, err
	}
	return &WorkdirCommand{
		Path:              req.args[0],
		CommandSourceCode: CommandSourceCode{req.original},
	}, nil

}

func parseRun(req parseRequest) (*RunCommand, error) {

	if err := req.flags.Parse(); err != nil {
		return nil, err
	}
	prependShell := false
	args := handleJSONArgs(req.args, req.attributes)
	if !req.attributes["json"] {
		prependShell = true
	}
	cmdFromArgs := strslice.StrSlice(args)

	return &RunCommand{
		Expression:        cmdFromArgs,
		PrependShell:      prependShell,
		CommandSourceCode: CommandSourceCode{req.original},
	}, nil

}

func parseCmd(req parseRequest) (*CmdCommand, error) {
	if err := req.flags.Parse(); err != nil {
		return nil, err
	}
	prependShell := false
	cmdSlice := handleJSONArgs(req.args, req.attributes)
	if !req.attributes["json"] {
		prependShell = true
	}

	return &CmdCommand{
		Cmd:               strslice.StrSlice(cmdSlice),
		PrependShell:      prependShell,
		CommandSourceCode: CommandSourceCode{req.original},
	}, nil

}

// parseOptInterval(flag) is the duration of flag.Value, or 0 if
// empty. An error is reported if the value is given and less than minimum duration.
func parseOptInterval(f *Flag) (time.Duration, error) {
	s := f.Value
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	if d < time.Duration(container.MinimumDuration) {
		return 0, fmt.Errorf("Interval %#v cannot be less than %s", f.name, container.MinimumDuration)
	}
	return d, nil
}
func parseHealthcheck(req parseRequest) (*HealthCheckCommand, error) {
	if len(req.args) == 0 {
		return nil, errAtLeastOneArgument("HEALTHCHECK")
	}
	cmd := &HealthCheckCommand{
		CommandSourceCode: CommandSourceCode{req.original},
	}

	typ := strings.ToUpper(req.args[0])
	args := req.args[1:]
	if typ == "NONE" {
		if len(args) != 0 {
			return nil, errors.New("HEALTHCHECK NONE takes no arguments")
		}
		test := strslice.StrSlice{typ}
		cmd.Health = &container.HealthConfig{
			Test: test,
		}
	} else {

		healthcheck := container.HealthConfig{}

		flInterval := req.flags.AddString("interval", "")
		flTimeout := req.flags.AddString("timeout", "")
		flStartPeriod := req.flags.AddString("start-period", "")
		flRetries := req.flags.AddString("retries", "")

		if err := req.flags.Parse(); err != nil {
			return nil, err
		}

		switch typ {
		case "CMD":
			cmdSlice := handleJSONArgs(args, req.attributes)
			if len(cmdSlice) == 0 {
				return nil, errors.New("Missing command after HEALTHCHECK CMD")
			}

			if !req.attributes["json"] {
				typ = "CMD-SHELL"
			}

			healthcheck.Test = strslice.StrSlice(append([]string{typ}, cmdSlice...))
		default:
			return nil, fmt.Errorf("Unknown type %#v in HEALTHCHECK (try CMD)", typ)
		}

		interval, err := parseOptInterval(flInterval)
		if err != nil {
			return nil, err
		}
		healthcheck.Interval = interval

		timeout, err := parseOptInterval(flTimeout)
		if err != nil {
			return nil, err
		}
		healthcheck.Timeout = timeout

		startPeriod, err := parseOptInterval(flStartPeriod)
		if err != nil {
			return nil, err
		}
		healthcheck.StartPeriod = startPeriod

		if flRetries.Value != "" {
			retries, err := strconv.ParseInt(flRetries.Value, 10, 32)
			if err != nil {
				return nil, err
			}
			if retries < 1 {
				return nil, fmt.Errorf("--retries must be at least 1 (not %d)", retries)
			}
			healthcheck.Retries = int(retries)
		} else {
			healthcheck.Retries = 0
		}

		cmd.Health = &healthcheck
	}
	return cmd, nil
}

func parseEntrypoint(req parseRequest) (*EntrypointCommand, error) {
	if err := req.flags.Parse(); err != nil {
		return nil, err
	}

	cmd := &EntrypointCommand{
		CommandSourceCode: CommandSourceCode{req.original},
	}

	parsed := handleJSONArgs(req.args, req.attributes)

	if len(parsed) != 0 {
		cmd.Cmd = strslice.StrSlice(parsed)
		if !req.attributes["json"] {
			cmd.PrependShell = true
		}
	}

	return cmd, nil
}

func parseExpose(req parseRequest) (*ExposeCommand, error) {
	portsTab := req.args

	if len(req.args) == 0 {
		return nil, errAtLeastOneArgument("EXPOSE")
	}

	if err := req.flags.Parse(); err != nil {
		return nil, err
	}

	ports, _, err := nat.ParsePortSpecs(portsTab)
	if err != nil {
		return nil, err
	}

	// instead of using ports directly, we build a list of ports and sort it so
	// the order is consistent. This prevents cache burst where map ordering
	// changes between builds
	portList := make([]string, len(ports))
	var i int
	for port := range ports {
		portList[i] = string(port)
		i++
	}
	sort.Strings(portList)
	return &ExposeCommand{
		Ports:             portList,
		CommandSourceCode: CommandSourceCode{req.original},
	}, nil
}

func parseUser(req parseRequest) (*UserCommand, error) {
	if len(req.args) != 1 {
		return nil, errExactlyOneArgument("USER")
	}

	if err := req.flags.Parse(); err != nil {
		return nil, err
	}
	return &UserCommand{
		User:              req.args[0],
		CommandSourceCode: CommandSourceCode{req.original},
	}, nil
}

func parseVolume(req parseRequest) (*VolumeCommand, error) {
	if len(req.args) == 0 {
		return nil, errAtLeastOneArgument("VOLUME")
	}

	if err := req.flags.Parse(); err != nil {
		return nil, err
	}

	cmd := &VolumeCommand{
		CommandSourceCode: CommandSourceCode{req.original},
	}

	for _, v := range req.args {
		v = strings.TrimSpace(v)
		if v == "" {
			return nil, errors.New("VOLUME specified can not be an empty string")
		}
		cmd.Volumes = append(cmd.Volumes, v)
	}
	return cmd, nil

}

func parseStopSignal(req parseRequest) (*StopSignalCommand, error) {
	if len(req.args) != 1 {
		return nil, errExactlyOneArgument("STOPSIGNAL")
	}
	sig := req.args[0]

	cmd := &StopSignalCommand{
		Signal:            sig,
		CommandSourceCode: CommandSourceCode{req.original},
	}
	return cmd, nil

}

func parseArg(req parseRequest) (*ArgCommand, error) {
	if len(req.args) != 1 {
		return nil, errExactlyOneArgument("ARG")
	}

	var (
		name       string
		newValue   string
		hasDefault bool
	)

	arg := req.args[0]
	// 'arg' can just be a name or name-value pair. Note that this is different
	// from 'env' that handles the split of name and value at the parser level.
	// The reason for doing it differently for 'arg' is that we support just
	// defining an arg and not assign it a value (while 'env' always expects a
	// name-value pair). If possible, it will be good to harmonize the two.
	if strings.Contains(arg, "=") {
		parts := strings.SplitN(arg, "=", 2)
		if len(parts[0]) == 0 {
			return nil, errBlankCommandNames("ARG")
		}

		name = parts[0]
		newValue = parts[1]
		hasDefault = true
	} else {
		name = arg
		hasDefault = false
	}

	var value *string
	if hasDefault {
		value = &newValue
	}

	return &ArgCommand{
		Name:              name,
		Value:             value,
		CommandSourceCode: CommandSourceCode{req.original},
	}, nil
}

func parseShell(req parseRequest) (*ShellCommand, error) {
	if err := req.flags.Parse(); err != nil {
		return nil, err
	}
	shellSlice := handleJSONArgs(req.args, req.attributes)
	switch {
	case len(shellSlice) == 0:
		// SHELL []
		return nil, errAtLeastOneArgument("SHELL")
	case req.attributes["json"]:
		// SHELL ["powershell", "-command"]

		return &ShellCommand{
			Shell:             strslice.StrSlice(shellSlice),
			CommandSourceCode: CommandSourceCode{req.original},
		}, nil
	default:
		// SHELL powershell -command - not JSON
		return nil, errNotJSON("SHELL", req.original)
	}
}

func errAtLeastOneArgument(command string) error {
	return fmt.Errorf("%s requires at least one argument", command)
}

func errExactlyOneArgument(command string) error {
	return fmt.Errorf("%s requires exactly one argument", command)
}

func errAtLeastTwoArguments(command string) error {
	return fmt.Errorf("%s requires at least two arguments", command)
}

func errBlankCommandNames(command string) error {
	return fmt.Errorf("%s names can not be blank", command)
}

func errTooManyArguments(command string) error {
	return fmt.Errorf("Bad input to %s, too many arguments", command)
}
