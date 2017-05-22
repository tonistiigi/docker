package instructions

import (
	"strings"
	"testing"

	"github.com/docker/docker/builder/dockerfile/command"
	"github.com/docker/docker/builder/dockerfile/parser"
	"github.com/docker/docker/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommandsExactlyOneArgument(t *testing.T) {
	commands := []string{
		"MAINTAINER",
		"WORKDIR",
		"USER",
		"STOPSIGNAL",
	}

	for _, command := range commands {
		ast, err := parser.Parse(strings.NewReader(command))
		if err != nil {
			t.Fatalf("Parsing error: %v", err.Error())
		}
		_, err = ParseCommand(ast.AST.Children[0])
		assert.EqualError(t, err, errExactlyOneArgument(command).Error())
	}
}

func TestCommandsAtLeastOneArgument(t *testing.T) {
	commands := []string{
		"ENV",
		"LABEL",
		"ONBUILD",
		"HEALTHCHECK",
		"EXPOSE",
		"VOLUME",
	}

	for _, command := range commands {
		ast, err := parser.Parse(strings.NewReader(command))
		if err != nil {
			t.Fatalf("Parsing error: %v", err.Error())
		}
		_, err = ParseCommand(ast.AST.Children[0])
		assert.EqualError(t, err, errAtLeastOneArgument(command).Error())
	}
}

func TestCommandsAtLeastTwoArgument(t *testing.T) {
	commands := []string{
		"ADD",
		"COPY",
	}

	for _, command := range commands {
		ast, err := parser.Parse(strings.NewReader(command + " arg1"))
		if err != nil {
			t.Fatalf("Parsing error: %v", err.Error())
		}
		_, err = ParseCommand(ast.AST.Children[0])
		assert.EqualError(t, err, errAtLeastTwoArguments(command).Error())
	}
}

func TestCommandsTooManyArguments(t *testing.T) {
	commands := []string{
		"ENV",
		"LABEL",
	}

	for _, command := range commands {
		node := &parser.Node{
			Original: command + "arg1 arg2 arg3",
			Value:    strings.ToLower(command),
			Next: &parser.Node{
				Value: "arg1",
				Next: &parser.Node{
					Value: "arg2",
					Next: &parser.Node{
						Value: "arg3",
					},
				},
			},
		}
		_, err := ParseCommand(node)
		assert.EqualError(t, err, errTooManyArguments(command).Error())
	}
}

func TestCommandsBlankNames(t *testing.T) {
	commands := []string{
		"ENV",
		"LABEL",
	}

	for _, command := range commands {
		node := &parser.Node{
			Original: command + " =arg2",
			Value:    strings.ToLower(command),
			Next: &parser.Node{
				Value: "",
				Next: &parser.Node{
					Value: "arg2",
				},
			},
		}
		_, err := ParseCommand(node)
		assert.EqualError(t, err, errBlankCommandNames(command).Error())
	}
}

func TestOnbuildIllegalTriggers(t *testing.T) {
	triggers := []struct{ command, expectedError string }{
		{"ONBUILD", "Chaining ONBUILD via `ONBUILD ONBUILD` isn't allowed"},
		{"MAINTAINER", "MAINTAINER isn't allowed as an ONBUILD trigger"},
		{"FROM", "FROM isn't allowed as an ONBUILD trigger"}}

	for _, trigger := range triggers {
		node := &parser.Node{
			Original: "ONBUILD " + trigger.command,
			Value:    "onbuild",
			Next: &parser.Node{
				Value: trigger.command,
			},
		}
		_, err := ParseCommand(node)
		assert.EqualError(t, err, trigger.expectedError)
	}
}

func TestHealthCheckCmd(t *testing.T) {
	node := &parser.Node{
		Value: command.Healthcheck,
		Next: &parser.Node{
			Value: "CMD",
			Next: &parser.Node{
				Value: "hello",
				Next: &parser.Node{
					Value: "world",
				},
			},
		},
	}
	cmd, err := ParseCommand(node)
	assert.NoError(t, err)
	hc, ok := cmd.(*HealthCheckCommand)
	assert.True(t, ok)
	expected := []string{"CMD-SHELL", "hello world"}
	assert.Equal(t, expected, hc.Health.Test)
}

func TestParseOptInterval(t *testing.T) {
	flInterval := &Flag{
		name:     "interval",
		flagType: stringType,
		Value:    "50ns",
	}
	_, err := parseOptInterval(flInterval)
	testutil.ErrorContains(t, err, "cannot be less than 1ms")

	flInterval.Value = "1ms"
	_, err = parseOptInterval(flInterval)
	require.NoError(t, err)
}
