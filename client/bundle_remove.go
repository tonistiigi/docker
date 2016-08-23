package client

import (
	"encoding/json"
	"net/url"

	"github.com/docker/docker/api/types"
	"golang.org/x/net/context"
)

// BundleRemove removes a bundle from the docker host.
func (cli *Client) BundleRemove(ctx context.Context, bundleID string, options types.BundleRemoveOptions) ([]types.BundleDelete, error) {
	query := url.Values{}

	if options.Force {
		query.Set("force", "1")
	}
	if !options.PruneChildren {
		query.Set("noprune", "1")
	}

	resp, err := cli.delete(ctx, "/bundles/"+bundleID, query, nil)
	if err != nil {
		return nil, err
	}

	var dels []types.BundleDelete
	err = json.NewDecoder(resp.body).Decode(&dels)
	ensureReaderClosed(resp)
	return dels, err
}
