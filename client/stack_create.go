package client

import (
	"encoding/json"
	"net/url"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/swarm"
	"golang.org/x/net/context"
)

// StackCreate creates a new Stack.
func (cli *Client) StackCreate(ctx context.Context, options types.StackCreateOptions) (swarm.StackCreateResponse, error) {
	query := url.Values{}
	query.Set("bundle", options.Bundle)
	query.Set("name", options.Name)

	var response swarm.StackCreateResponse
	resp, err := cli.post(ctx, "/stacks/create", query, nil, nil)
	if err != nil {
		return response, err
	}

	err = json.NewDecoder(resp.body).Decode(&response)
	ensureReaderClosed(resp)
	return response, err
}
