package bundle

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/docker/docker/api/server/httputils"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/pkg/ioutils"
	"github.com/docker/docker/pkg/streamformatter"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
)

// Creates a bundle from configuration of by pulling
func (s *bundleRouter) postBundleCreate(ctx context.Context, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if err := httputils.ParseForm(r); err != nil {
		return err
	}
	var (
		bundle = r.Form.Get("fromBundle")
		repo   = r.Form.Get("repo")
		tag    = r.Form.Get("tag")
		err    error
		output = ioutils.NewWriteFlusher(w)
	)
	defer output.Close()

	w.Header().Set("Content-Type", "application/json")

	if bundle != "" { //pull
		metaHeaders := map[string][]string{}
		for k, v := range r.Header {
			if strings.HasPrefix(k, "X-Meta-") {
				metaHeaders[k] = v
			}
		}

		var authConfig types.AuthConfig
		if authEncoded := r.Header.Get("X-Registry-Auth"); authEncoded != "" {
			decoded, err := base64.URLEncoding.DecodeString(authEncoded)
			if err != nil {
				return errors.Wrap(err, "failed to decode base64 auth") // don't include auth data in error
			}
			if err := json.Unmarshal([]byte(decoded), &authConfig); err != nil {
				return errors.Wrap(err, "failed to decode json auth")
			}
		}

		err = errors.Wrapf(s.backend.PullBundle(ctx, bundle, tag, metaHeaders, &authConfig, output), "failed to pull bundle %v %v", bundle, tag)
	} else { // create
		err = errors.Wrapf(s.backend.CreateBundle(r.Form.Get("fromSrc"), repo, tag, r.Body, output), "failed to create bundle from %v", r.Form.Get("fromSrc"))
	}
	if err != nil {
		if !output.Flushed() {
			return err
		}
		sf := streamformatter.NewJSONStreamFormatter()
		output.Write(sf.FormatError(err))
	}

	return nil
}

func (s *bundleRouter) postBundlesPush(ctx context.Context, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	metaHeaders := map[string][]string{}
	for k, v := range r.Header {
		if strings.HasPrefix(k, "X-Meta-") {
			metaHeaders[k] = v
		}
	}
	if err := httputils.ParseForm(r); err != nil {
		return err
	}

	var authConfig types.AuthConfig
	if authEncoded := r.Header.Get("X-Registry-Auth"); authEncoded != "" {
		decoded, err := base64.URLEncoding.DecodeString(authEncoded)
		if err != nil {
			return errors.Wrap(err, "failed to decode base64 auth") // don't include auth data in error
		}
		if err := json.Unmarshal([]byte(decoded), &authConfig); err != nil {
			return errors.Wrap(err, "failed to decode json auth")
		}
	}

	bundle := vars["name"]
	tag := r.Form.Get("tag")

	output := ioutils.NewWriteFlusher(w)
	defer output.Close()

	w.Header().Set("Content-Type", "application/json")

	if err := s.backend.PushBundle(ctx, bundle, tag, metaHeaders, &authConfig, output); err != nil {
		err = errors.Wrapf(err, "failed to push bundle %v %v", bundle, tag)
		if !output.Flushed() {
			return err
		}
		sf := streamformatter.NewJSONStreamFormatter()
		output.Write(sf.FormatError(err))
	}
	return nil
}

func (s *bundleRouter) deleteBundles(ctx context.Context, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if err := httputils.ParseForm(r); err != nil {
		return errors.Wrap(err, "failed to parse request for bundle delete")
	}

	name := vars["name"]

	if strings.TrimSpace(name) == "" {
		return errors.New("bundle name cannot be blank")
	}

	force := httputils.BoolValue(r, "force")
	prune := !httputils.BoolValue(r, "noprune")

	list, err := s.backend.BundleDelete(name, force, prune)
	if err != nil {
		return errors.Wrapf(err, "failed to delete bundle %v", name)
	}

	return httputils.WriteJSON(w, http.StatusOK, list)
}

func (s *bundleRouter) getBundlesByName(ctx context.Context, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	bundleInspect, err := s.backend.LookupBundle(vars["name"])
	if err != nil {
		return errors.Wrapf(err, "failed to lookup bundle %v", vars["name"])
	}

	return httputils.WriteJSON(w, http.StatusOK, bundleInspect)
}

func (s *bundleRouter) getBundlesJSON(ctx context.Context, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if err := httputils.ParseForm(r); err != nil {
		return errors.Wrap(err, "failed to parse request for bundle ls")
	}

	bundles, err := s.backend.Bundles(r.Form.Get("filters"), r.Form.Get("filter"))
	if err != nil {
		return errors.Wrap(err, "failed to list bundles")
	}

	return httputils.WriteJSON(w, http.StatusOK, bundles)
}

func (s *bundleRouter) postBundlesTag(ctx context.Context, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if err := httputils.ParseForm(r); err != nil {
		return errors.Wrap(err, "failed to parse request for bundle tag")
	}
	if err := s.backend.TagBundle(vars["name"], r.Form.Get("repo"), r.Form.Get("tag")); err != nil {
		return errors.Wrap(err, "failed to tag bundle")
	}
	w.WriteHeader(http.StatusCreated)
	return nil
}
