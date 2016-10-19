package cluster

import (
	apitypes "github.com/docker/docker/api/types"
	types "github.com/docker/docker/api/types/swarm"
	"github.com/docker/docker/daemon/cluster/convert"
	swarmapi "github.com/docker/swarmkit/api"
)

// GetSecret returns a secret from a managed swarm cluster
func (c *Cluster) GetSecret(id string) (types.Secret, error) {
	ctx, cancel := c.getRequestContext()
	defer cancel()

	r, err := c.node.client.GetSecret(ctx, &swarmapi.GetSecretRequest{SecretID: id})
	if err != nil {
		return types.Secret{}, err
	}

	return convert.SecretFromGRPC(r.Secret), nil
}

// GetSecrets returns all secrets of a managed swarm cluster.
func (c *Cluster) GetSecrets(options apitypes.SecretListOptions) ([]types.Secret, error) {
	c.RLock()
	defer c.RUnlock()

	if !c.isActiveManager() {
		return nil, c.errNoManager()
	}

	filters, err := newListSecretsFilters(options.Filter)
	if err != nil {
		return nil, err
	}
	ctx, cancel := c.getRequestContext()
	defer cancel()

	r, err := c.node.client.ListSecrets(ctx,
		&swarmapi.ListSecretsRequest{Filters: filters})
	if err != nil {
		return nil, err
	}

	secrets := []types.Secret{}

	for _, secret := range r.Secrets {
		secrets = append(secrets, convert.SecretFromGRPC(secret))
	}

	return secrets, nil
}

// CreateSecret creates a new secret in a managed swarm cluster.
func (c *Cluster) CreateSecret(s types.SecretSpec) (string, error) {
	c.RLock()
	defer c.RUnlock()

	if !c.isActiveManager() {
		return "", c.errNoManager()
	}

	ctx, cancel := c.getRequestContext()
	defer cancel()

	secretSpec, err := convert.SecretSpecToGRPC(s)
	if err != nil {
		return "", err
	}

	r, err := c.node.client.CreateSecret(ctx,
		&swarmapi.CreateSecretRequest{Spec: &secretSpec})
	if err != nil {
		return "", err
	}

	return r.Secret.ID, nil
}

// RemoveSecret removes a secret from a managed swarm cluster.
func (c *Cluster) RemoveSecret(id string) error {
	c.RLock()
	defer c.RUnlock()

	if !c.isActiveManager() {
		return c.errNoManager()
	}

	ctx, cancel := c.getRequestContext()
	defer cancel()

	req := &swarmapi.RemoveSecretRequest{
		SecretID: id,
	}

	if _, err := c.node.client.RemoveSecret(ctx, req); err != nil {
		return err
	}
	return nil
}
