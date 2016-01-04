package client

import (
	"encoding/json"

	"github.com/docker/engine-api/types"
)

// PluginList returns the plugins configured in the docker host.
func (cli *Client) PluginList() (types.PluginsListResponse, error) {
	var plugins types.PluginsListResponse
	resp, err := cli.get("/plugin/_list", nil, nil)
	if err != nil {
		return plugins, err
	}

	err = json.NewDecoder(resp.body).Decode(&plugins)
	ensureReaderClosed(resp)
	return plugins, err
}

// PluginRemove removes a plugin from the docker host.
func (cli *Client) PluginRemove(name string) error {
	resp, err := cli.delete("/plugin/"+name, nil, nil)
	ensureReaderClosed(resp)
	return err
}

func (cli *Client) PluginActivate(name string) error {
	resp, err := cli.post("/plugin/"+name+"/activate", nil, nil, nil)
	ensureReaderClosed(resp)
	return err
}

func (cli *Client) PluginDisable(name string) error {
	resp, err := cli.post("/plugin/"+name+"/disable", nil, nil, nil)
	ensureReaderClosed(resp)
	return err
}

func (cli *Client) PluginLoad(name, version string) error {
	resp, err := cli.post("/plugin/_load/"+name+"/"+version, nil, nil, nil)
	ensureReaderClosed(resp)
	return err
}
