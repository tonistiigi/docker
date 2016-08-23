package client

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"net/http"

	"github.com/docker/docker/api/types"
	"golang.org/x/net/context"
)

// BundleInspectWithRaw returns the bundle information and its raw representation.
func (cli *Client) BundleInspectWithRaw(ctx context.Context, bundleID string) (types.BundleInspect, []byte, error) {
	serverResp, err := cli.get(ctx, "/bundles/"+bundleID, nil, nil)
	if err != nil {
		if serverResp.statusCode == http.StatusNotFound {
			return types.BundleInspect{}, nil, bundleNotFoundError{bundleID}
		}
		return types.BundleInspect{}, nil, err
	}
	defer ensureReaderClosed(serverResp)

	body, err := ioutil.ReadAll(serverResp.body)
	if err != nil {
		return types.BundleInspect{}, nil, err
	}

	var response types.BundleInspect
	rdr := bytes.NewReader(body)
	err = json.NewDecoder(rdr).Decode(&response)
	return response, body, err
}
