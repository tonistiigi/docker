package client

import (
	"errors"
	"fmt"
	"net/url"

	"golang.org/x/net/context"

	distreference "github.com/docker/distribution/reference"
	"github.com/docker/docker/api/types/reference"
)

// BundleTag tags a bundle in the docker host
func (cli *Client) BundleTag(ctx context.Context, bundleID, ref string) error {
	distributionRef, err := distreference.ParseNamed(ref)
	if err != nil {
		return fmt.Errorf("Error parsing reference: %q is not a valid repository/tag", ref)
	}

	if _, isCanonical := distributionRef.(distreference.Canonical); isCanonical {
		return errors.New("refusing to create a tag with a digest reference")
	}

	tag := reference.GetTagFromNamedRef(distributionRef)

	query := url.Values{}
	query.Set("repo", distributionRef.Name())
	query.Set("tag", tag)

	resp, err := cli.post(ctx, "/bundles/"+bundleID+"/tag", query, nil, nil)
	ensureReaderClosed(resp)
	return err
}
