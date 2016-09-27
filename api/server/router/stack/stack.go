package stack

import "github.com/docker/docker/api/server/router"

// stackRouter is a router to talk with the stack controller
type stackRouter struct {
	backend Backend
	routes  []router.Route
}

// NewRouter initializes a new stack router
func NewRouter(backend Backend) router.Router {
	r := &stackRouter{
		backend: backend,
	}
	r.initRoutes()
	return r
}

// Routes returns the available routes to the stack controller
func (r *stackRouter) Routes() []router.Route {
	return r.routes
}

// initRoutes initializes the routes in the stack router
func (r *stackRouter) initRoutes() {
	r.routes = []router.Route{
		// GET
		// router.NewGetRoute("/stacks/json", r.getStacksJSON),
		// router.NewGetRoute("/stacks/{name:.*}/json", r.getStacksByName),
		// POST
		router.NewPostRoute("/stacks/create", r.postStackCreate),
		// DELETE
		// router.NewDeleteRoute("/stacks/{name:.*}", r.deleteStacks),
	}
}
