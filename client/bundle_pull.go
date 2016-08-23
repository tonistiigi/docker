package client

import (
	"io"
	"net/http"
	"net/url"

	"golang.org/x/net/context"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/reference"
)

// BundlePull requests the docker host to pull a bundle from a remote registry.
// It executes the privileged function if the operation is unauthorized
// and it tries one more time.
// It's up to the caller to handle the io.ReadCloser and close it properly.
//
// FIXME(vdemeester): this is currently used in a few way in docker/docker
// - if not in trusted content, ref is used to pass the whole reference, and tag is empty
// - if in trusted content, ref is used to pass the reference name, and tag for the digest
func (cli *Client) BundlePull(ctx context.Context, ref string, options types.BundlePullOptions) (io.ReadCloser, error) {
	repository, tag, err := reference.Parse(ref)
	if err != nil {
		return nil, err
	}

	query := url.Values{}
	query.Set("fromBundle", repository)
	if tag != "" {
		query.Set("tag", tag)
	}

	resp, err := cli.tryBundleCreate(ctx, query, options.RegistryAuth)
	if resp.statusCode == http.StatusUnauthorized && options.PrivilegeFunc != nil {
		newAuthHeader, privilegeErr := options.PrivilegeFunc()
		if privilegeErr != nil {
			return nil, privilegeErr
		}
		resp, err = cli.tryBundleCreate(ctx, query, newAuthHeader)
	}
	if err != nil {
		return nil, err
	}
	return resp.body, nil
}
