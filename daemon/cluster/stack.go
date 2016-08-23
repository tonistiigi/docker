package cluster

import (
	"fmt"

	types "github.com/docker/docker/api/types/swarm"
	"github.com/docker/docker/daemon/cluster/convert"
	"github.com/docker/docker/pkg/namesgenerator"
	swarmapi "github.com/docker/swarmkit/api"
)

const labelNamespace = "com.docker.stack.namespace"

// CreateStack(name, bundle string) error // TODO: add config

func (c *Cluster) CreateStack(name, bundleRef string) (*types.StackCreateResponse, error) {
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

	b, err := c.config.Backend.ResolveBundleManifest(bundleRef)
	if err != nil {
		return nil, err
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
					Env:     s.Env, // TODO: missing fields. figure out bundle.ServiceSpec type first
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
