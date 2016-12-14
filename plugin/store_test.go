package plugin

import (
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/plugin"
)

func TestFilterByCapNeg(t *testing.T) {
	p := plugin.NewPlugin("test", "1234567890", "/run/docker", "/var/lib/docker/plugins", "latest")

	iType := types.PluginInterfaceType{"volumedriver", "docker", "1.0"}
	i := types.PluginConfigInterface{"plugins.sock", []types.PluginInterfaceType{iType}}
	p.PluginObj.Config.Interface = i

	_, err := p.FilterByCap("foobar")
	if err == nil {
		t.Fatalf("expected inadequate error, got %v", err)
	}
}

func TestFilterByCapPos(t *testing.T) {
	p := plugin.NewPlugin("test", "1234567890", "/run/docker", "/var/lib/docker/plugins", "latest")

	iType := types.PluginInterfaceType{"volumedriver", "docker", "1.0"}
	i := types.PluginConfigInterface{"plugins.sock", []types.PluginInterfaceType{iType}}
	p.PluginObj.Config.Interface = i

	_, err := p.FilterByCap("volumedriver")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}
