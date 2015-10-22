package metadata

import (
	"encoding/json"

	"github.com/docker/distribution/digest"
	"github.com/docker/docker/layer"
)

// BlobSumStorageService maps layer IDs to a set of known blobsums for
// the layer.
type BlobSumStorageService struct {
	store Store
}

// maxBlobSums is the number of blobsums to keep per layer DiffID.
const maxBlobSums = 5

// NewBlobSumStorageService creates a new blobsum mapping service.
func NewBlobSumStorageService(store Store) *BlobSumStorageService {
	return &BlobSumStorageService{
		store: store,
	}
}

// namespace returns the namespace used by this service.
func (blobserv *BlobSumStorageService) namespace() string {
	return "blobsum-storage"
}

func (blobserv *BlobSumStorageService) key(diffID layer.DiffID) string {
	return string(digest.Digest(diffID).Algorithm()) + "/" + digest.Digest(diffID).Hex()
}

// Get finds the blobsums associated with a layer DiffID.
func (blobserv *BlobSumStorageService) Get(diffID layer.DiffID) ([]digest.Digest, error) {
	jsonBytes, err := blobserv.store.Get(blobserv.namespace(), blobserv.key(diffID))
	if err != nil {
		return nil, err
	}

	var blobsums []digest.Digest
	if err := json.Unmarshal(jsonBytes, &blobsums); err != nil {
		return nil, err
	}

	return blobsums, nil
}

// Add associates a blobsum with a layer DiffID. If too many blobsums are
// present, the oldest one is dropped.
func (blobserv *BlobSumStorageService) Add(diffID layer.DiffID, blobsum digest.Digest) error {
	oldBlobSums, err := blobserv.Get(diffID)
	if err != nil {
		oldBlobSums = nil
	}
	newBlobSums := make([]digest.Digest, 0, len(oldBlobSums)+1)

	// Copy all other blobsums to new slice
	for _, oldSum := range oldBlobSums {
		if oldSum != blobsum {
			newBlobSums = append(newBlobSums, oldSum)
		}
	}

	newBlobSums = append(newBlobSums, blobsum)

	if len(newBlobSums) > maxBlobSums {
		newBlobSums = newBlobSums[len(newBlobSums)-maxBlobSums : len(newBlobSums)]
	}

	jsonBytes, err := json.Marshal(newBlobSums)
	if err != nil {
		return err
	}

	return blobserv.store.Set(blobserv.namespace(), blobserv.key(diffID), jsonBytes)
}
