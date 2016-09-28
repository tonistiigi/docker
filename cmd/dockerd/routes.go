// +build !experimental

package main

import (
	"github.com/docker/docker/api/server/httputils"
	"github.com/docker/docker/api/server/router"
	"github.com/docker/docker/daemon"
	"github.com/docker/docker/daemon/cluster"
)

func addExperimentalRouters(routers []router.Router, d *daemon.Daemon, c *cluster.Cluster, decoder httputils.ContainerDecoder) []router.Router {
	return routers
}
