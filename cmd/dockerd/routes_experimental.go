// +build experimental

package main

import (
	"github.com/docker/docker/api/server/httputils"
	"github.com/docker/docker/api/server/router"
	"github.com/docker/docker/api/server/router/bundle"
	checkpointrouter "github.com/docker/docker/api/server/router/checkpoint"
	pluginrouter "github.com/docker/docker/api/server/router/plugin"
	"github.com/docker/docker/api/server/router/stack"
	"github.com/docker/docker/daemon"
	"github.com/docker/docker/daemon/cluster"
	"github.com/docker/docker/plugin"
)

func addExperimentalRouters(routers []router.Router, d *daemon.Daemon, c *cluster.Cluster, decoder httputils.ContainerDecoder) []router.Router {
	return append(routers,
		checkpointrouter.NewRouter(d, decoder),
		pluginrouter.NewRouter(plugin.GetManager()),
		bundle.NewRouter(d),
		stack.NewRouter(c),
	)
}
