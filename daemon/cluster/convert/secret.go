package convert

import (
	"github.com/Sirupsen/logrus"
	types "github.com/docker/docker/api/types/swarm"
	swarmapi "github.com/docker/swarmkit/api"
	"github.com/docker/swarmkit/protobuf/ptypes"
)

// SecretFromGRPC converts a grpc Service to a Service.
func SecretFromGRPC(s *swarmapi.Secret) types.Secret {
	logrus.Debugf("%+v", s)
	secret := types.Secret{
		ID:         s.ID,
		Digest:     s.Digest,
		SecretSize: s.SecretSize,
	}

	// Meta
	secret.Version.Index = s.Meta.Version.Index
	secret.CreatedAt, _ = ptypes.Timestamp(s.Meta.CreatedAt)
	secret.UpdatedAt, _ = ptypes.Timestamp(s.Meta.UpdatedAt)

	secret.Spec = &types.SecretSpec{
		Annotations: types.Annotations{
			Name:   s.Spec.Annotations.Name,
			Labels: s.Spec.Annotations.Labels,
		},
	}

	return secret
}

// SecretFromGRPC converts a grpc Service to a Service.
func SecretSpecToGRPC(s types.SecretSpec) (swarmapi.SecretSpec, error) {
	spec := swarmapi.SecretSpec{
		Annotations: swarmapi.Annotations{
			Name:   s.Name,
			Labels: s.Labels,
		},
		Data: s.Data,
	}

	return spec, nil
}
