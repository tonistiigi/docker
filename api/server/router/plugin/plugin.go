package plugin

import "github.com/docker/docker/api/server/router"

// pluginRouter is a router to talk with the plugin controller
type pluginRouter struct {
	backend Backend
	routes  []router.Route
}

// NewRouter initializes a new plugin router
func NewRouter(b Backend) router.Router {
	r := &pluginRouter{
		backend: b,
	}
	r.initRoutes()
	return r
}

// Routes returns the available routers to the plugin controller
func (r *pluginRouter) Routes() []router.Route {
	return r.routes
}

func (r *pluginRouter) initRoutes() {
	r.routes = []router.Route{
		router.NewGetRoute("/plugin/_list", r.listPlugins),
		router.NewPostRoute("/plugin/_load/{name:.*}/{tag:.*}", r.loadPlugin),
		router.NewPostRoute("/plugin/{name:.*}/activate", r.activatePlugin), // PATCH
		router.NewPostRoute("/plugin/{name:.*}/disable", r.disablePlugin),
		router.NewDeleteRoute("/plugin/{name:.*}", r.deletePlugin),
		// set

		// todo:
		// GET /plugins
		// POST /plugins {name:,version}
		// PATCH /plugins/name {active:}
		// DELETE /plugins/name
	}
}
