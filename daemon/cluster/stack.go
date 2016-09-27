package cluster

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	apitypes "github.com/docker/docker/api/types"
	types "github.com/docker/docker/api/types/swarm"
	"github.com/docker/docker/daemon/cluster/convert"
	"github.com/docker/docker/pkg/namesgenerator"
	swarmapi "github.com/docker/swarmkit/api"
	"github.com/pkg/errors"
)

const labelNamespace = "com.docker.stack.namespace"

// TODO: add config

func (c *Cluster) CreateStack(name, bundleRef, encodedAuth string) (*types.StackCreateResponse, error) {
	c.RLock()
	defer c.RUnlock()

	if !c.isActiveManager() {
		return nil, c.errNoManager()
	}

	if name == "" {
		name = namesgenerator.GetRandomName(0)
	}

	if bundleRef == "" {
		return nil, fmt.Errorf("bundle name cannot be empty")
	}

	var authConfig apitypes.AuthConfig
	if encodedAuth != "" {
		authJSON, err := base64.URLEncoding.DecodeString(encodedAuth)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to decode base64 auth %q", encodedAuth)
		}
		if err := json.Unmarshal(authJSON, &authConfig); err != nil {
			return nil, errors.Wrapf(err, "failed to parse auth json: %v", string(authJSON))
		}
	}

	b, err := c.config.Backend.ResolveBundleManifest(bundleRef, &authConfig)
	if err != nil {
		return nil, errors.Wrapf(err, "error resolving bundle %v", bundleRef)
	}

	resp := &types.StackCreateResponse{}

	for _, s := range b.Services {
		one := uint64(1)
		serviceSpec, err := convert.ServiceSpecToGRPC(types.ServiceSpec{
			Annotations: types.Annotations{
				Name:   name + "_" + s.Name,
				Labels: getStackLabels(name, b.Labels),
			},
			TaskTemplate: types.TaskSpec{
				ContainerSpec: types.ContainerSpec{
					Image:   s.Name,
					Labels:  s.Labels,
					Command: s.Command,
					Args:    s.Args,
					Env:     s.Env,
					Dir:     s.WorkingDir,
					User:    s.User,
				},
			},
			Mode: types.ServiceMode{
				Replicated: &types.ReplicatedService{
					Replicas: &one,
				},
			},
		})
		if err != nil {
			return nil, err
		}

		ctnr := serviceSpec.Task.GetContainer()
		if ctnr == nil {
			return nil, fmt.Errorf("service does not use container tasks")
		}
		ctnr.Bundle = bundleRef
		ctnr.PullOptions = &swarmapi.ContainerSpec_PullOptions{RegistryAuth: encodedAuth}

		ctx, cancel := c.getRequestContext()
		defer cancel()
		r, err := c.client.CreateService(ctx, &swarmapi.CreateServiceRequest{Spec: &serviceSpec})
		if err != nil {
			return nil, err
		}

		resp.ServiceIDs = append(resp.ServiceIDs, r.Service.ID)
	}

	return resp, nil
}

func getStackLabels(namespace string, labels map[string]string) map[string]string {
	if labels == nil {
		labels = make(map[string]string)
	}
	labels[labelNamespace] = namespace
	return labels
}
