package bundle

import "github.com/docker/docker/api/server/router"

// bundleRouter is a router to talk with the bundle controller
type bundleRouter struct {
	backend Backend
	routes  []router.Route
}

// NewRouter initializes a new bundle router
func NewRouter(backend Backend) router.Router {
	r := &bundleRouter{
		backend: backend,
	}
	r.initRoutes()
	return r
}

// Routes returns the available routes to the bundle controller
func (r *bundleRouter) Routes() []router.Route {
	return r.routes
}

// initRoutes initializes the routes in the bundle router
func (r *bundleRouter) initRoutes() {
	r.routes = []router.Route{
		// GET
		router.NewGetRoute("/bundles", r.getBundlesJSON),
		router.NewGetRoute("/bundles/{name:.*}", r.getBundlesByName),
		// POST
		router.Cancellable(router.NewPostRoute("/bundles/create", r.postBundleCreate)),
		router.Cancellable(router.NewPostRoute("/bundles/{name:.*}/push", r.postBundlesPush)),
		router.NewPostRoute("/bundles/{name:.*}/tag", r.postBundlesTag),
		// DELETE
		router.NewDeleteRoute("/bundles/{name:.*}", r.deleteBundles),
	}
}
