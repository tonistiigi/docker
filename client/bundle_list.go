package client

import (
	"encoding/json"
	"net/url"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"golang.org/x/net/context"
)

// BundleList returns a list of bundles in the docker host.
func (cli *Client) BundleList(ctx context.Context, options types.BundleListOptions) ([]types.Bundle, error) {
	query := url.Values{}

	if options.Filter.Len() > 0 {
		filterJSON, err := filters.ToParamWithVersion(cli.version, options.Filter)
		if err != nil {
			return nil, err
		}
		query.Set("filters", filterJSON)
	}

	serverResp, err := cli.get(ctx, "/bundles", query, nil)
	if err != nil {
		return nil, err
	}

	var bundles []types.Bundle
	err = json.NewDecoder(serverResp.body).Decode(&bundles)
	ensureReaderClosed(serverResp)
	return bundles, err
}
