package client

import (
	"net/url"

	"golang.org/x/net/context"
)

func (cli *Client) tryBundleCreate(ctx context.Context, query url.Values, registryAuth string) (serverResponse, error) {
	headers := map[string][]string{"X-Registry-Auth": {registryAuth}}
	return cli.post(ctx, "/bundles/create", query, nil, headers)
}
