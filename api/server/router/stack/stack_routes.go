package stack

import (
	"fmt"
	"net/http"

	"github.com/docker/docker/api/server/httputils"
	"golang.org/x/net/context"
)

// Creates a stack from configuration of by pulling
func (s *stackRouter) postStackCreate(ctx context.Context, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if err := httputils.ParseForm(r); err != nil {
		return err
	}
	bundle := r.Form.Get("bundle")
	name := r.Form.Get("name")

	// Get returns "" if the header does not exist
	encodedAuth := r.Header.Get("X-Registry-Auth")

	resp, err := s.backend.CreateStack(name, bundle, encodedAuth)
	if err != nil {
		return err
	}

	return httputils.WriteJSON(w, http.StatusCreated, resp)
}

func (s *stackRouter) deleteStacks(ctx context.Context, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	return fmt.Errorf("not implemented")
}
