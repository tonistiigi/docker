package metadata

import (
	"github.com/docker/distribution/digest"
	"github.com/docker/docker/layer"
)

// BlobSumLookupService maps a set of blobsums to a layer ID.
type BlobSumLookupService struct {
	store Store
}

// NewBlobSumLookupService creates a new blobsum mapping service.
func NewBlobSumLookupService(store Store) *BlobSumLookupService {
	return &BlobSumLookupService{
		store: store,
	}
}

// namespace returns the namespace used by this service.
func (blobserv *BlobSumLookupService) namespace() string {
	return "blobsum-lookup"
}

// hash computes a cryptographic hash over the list of digests to get a key
// representing those digests.
func (blobserv *BlobSumLookupService) hash(blobsums []digest.Digest) (digest.Digest, error) {
	var toHash []byte
	first := true

	for _, blobsum := range blobsums {
		if !first {
			toHash = append(toHash, byte(' '))
			first = false
		}
		toHash = append(toHash, blobsum...)
	}

	return digest.FromBytes(toHash)
}

// Get finds a layer address from a slice of blobsums. The blobsums must be
// ordered from bottom-most to top-most.
func (blobserv *BlobSumLookupService) Get(blobsums []digest.Digest) (layer.ID, error) {
	key, err := blobserv.hash(blobsums)
	if err != nil {
		return layer.ID(""), err
	}

	addressBytes, err := blobserv.store.Get(blobserv.namespace(), string(key))
	if err != nil {
		return layer.ID(""), err
	}

	return layer.ID(addressBytes), nil
}

// Set associates a layer ID with a blobsum.
func (blobserv *BlobSumLookupService) Set(blobsums []digest.Digest, addr layer.ID) error {
	key, err := blobserv.hash(blobsums)
	if err != nil {
		return err
	}

	return blobserv.store.Set(blobserv.namespace(), string(key), []byte(addr))
}
