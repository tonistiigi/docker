package bundle

import (
	"io"

	"github.com/docker/docker/api/types"
	"golang.org/x/net/context"
)

// Backend is all the methods that need to be implemented
// to provide bundle specific functionality.
type Backend interface {
	CreateBundle(src, repository, tag string, inConfig io.ReadCloser, outStream io.Writer) error
	PullBundle(ctx context.Context, bundle, tag string, metaHeaders map[string][]string, authConfig *types.AuthConfig, outStream io.Writer) error
	PushBundle(ctx context.Context, bundle, tag string, metaHeaders map[string][]string, authConfig *types.AuthConfig, outStream io.Writer) error
	BundleDelete(bundleRef string, force, prune bool) ([]types.BundleDelete, error)
	Bundles(filterArgs string, filter string) ([]*types.Bundle, error)
	LookupBundle(name string) (*types.BundleInspect, error)
	TagBundle(bundleName, repository, tag string) error
}
