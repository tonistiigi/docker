package metadata

import (
	"io/ioutil"
	"os"
	"testing"

	"github.com/docker/distribution/digest"
	"github.com/docker/docker/layer"
)

func TestBlobLookupService(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "blobsum-lookup-service-test")
	if err != nil {
		t.Fatalf("could not create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	metadataStore := NewFSMetadataStore(tmpDir)
	lookupService := NewBlobSumLookupService(metadataStore)

	testVectors := []struct {
		blobsums []digest.Digest
		layerID  layer.ID
	}{
		{
			blobsums: []digest.Digest{
				digest.Digest("sha256:f0cd5ca10b07f35512fc2f1cbf9a6cefbdb5cba70ac6b0c9e5988f4497f71937"),
			},
			layerID: layer.ID("sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4"),
		},
		{
			blobsums: []digest.Digest{
				digest.Digest("sha256:f0cd5ca10b07f35512fc2f1cbf9a6cefbdb5cba70ac6b0c9e5988f4497f71937"),
				digest.Digest("sha256:9e3447ca24cb96d86ebd5960cb34d1299b07e0a0e03801d90b9969a2c187dd6e"),
			},
			layerID: layer.ID("sha256:86e0e091d0da6bde2456dbb48306f3956bbeb2eae1b5b9a43045843f69fe4aaa"),
		},
		{
			blobsums: []digest.Digest{
				digest.Digest("sha256:f0cd5ca10b07f35512fc2f1cbf9a6cefbdb5cba70ac6b0c9e5988f4497f71937"),
				digest.Digest("sha256:9e3447ca24cb96d86ebd5960cb34d1299b07e0a0e03801d90b9969a2c187dd6e"),
				digest.Digest("sha256:cbbf2f9a99b47fc460d422812b6a5adff7dfee951d8fa2e4a98caa0382cfbdbf"),
			},
			layerID: layer.ID("sha256:03f4658f8b782e12230c1783426bd3bacce651ce582a4ffb6fbbfa2079428ecb"),
		},
	}

	// Set some associations
	for _, vec := range testVectors {
		err := lookupService.Set(vec.blobsums, vec.layerID)
		if err != nil {
			t.Fatalf("error calling Set: %v", err)
		}
	}

	// Check the correct values are read back
	for _, vec := range testVectors {
		layerID, err := lookupService.Get(vec.blobsums)
		if err != nil {
			t.Fatalf("error calling Get: %v", err)
		}
		if layerID != vec.layerID {
			t.Fatal("Get returned incorrect layer ID")
		}
	}

	// Test Get on a nonexistent entry
	_, err = lookupService.Get([]digest.Digest{digest.Digest("sha256:82379823067823853223359023576437723560923756b03560378f4497753917")})
	if err == nil {
		t.Fatal("expected error looking up nonexistent entry")
	}

	// Overwrite one of the entries and read it back
	err = lookupService.Set(testVectors[0].blobsums, testVectors[1].layerID)
	if err != nil {
		t.Fatalf("error calling Set: %v", err)
	}
	layerID, err := lookupService.Get(testVectors[0].blobsums)
	if err != nil {
		t.Fatalf("error calling Get: %v", err)
	}
	if layerID != testVectors[1].layerID {
		t.Fatal("Get returned incorrect layer ID")
	}
}
