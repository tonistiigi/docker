package plugin

import (
	"net/http"

	"github.com/docker/docker/api/server/httputils"
	"golang.org/x/net/context"
)

func (pr *pluginRouter) loadPlugin(ctx context.Context, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	return pr.backend.Load(vars["name"], vars["tag"])
}

func (pr *pluginRouter) activatePlugin(ctx context.Context, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	return pr.backend.Activate(vars["name"])
}

func (pr *pluginRouter) disablePlugin(ctx context.Context, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	return pr.backend.Disable(vars["name"])
}
func (pr *pluginRouter) deletePlugin(ctx context.Context, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	return pr.backend.Delete(vars["name"])
}

func (pr *pluginRouter) listPlugins(ctx context.Context, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	l, err := pr.backend.List()
	if err != nil {
		return err
	}
	return httputils.WriteJSON(w, http.StatusOK, l)
}
