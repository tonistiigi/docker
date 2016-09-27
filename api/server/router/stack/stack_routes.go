package stack

import (
	"net/http"

	"github.com/docker/docker/api/server/httputils"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
)

// Creates a stack from configuration of by pulling
func (s *stackRouter) postStackCreate(ctx context.Context, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if err := httputils.ParseForm(r); err != nil {
		return errors.Wrap(err, "failed to parse request for stack deploy")
	}
	bundle := r.Form.Get("bundle")
	name := r.Form.Get("name")

	// Get returns "" if the header does not exist
	encodedAuth := r.Header.Get("X-Registry-Auth")

	resp, err := s.backend.CreateStack(name, bundle, encodedAuth)
	if err != nil {
		return errors.Wrap(err, "failed to create stack")
	}

	return httputils.WriteJSON(w, http.StatusCreated, resp)
}
