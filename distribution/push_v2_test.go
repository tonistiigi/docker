package distribution

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/docker/distribution/digest"
	"github.com/docker/distribution/manifest"
	"github.com/docker/docker/image"
)

func TestCreateV2Manifest(t *testing.T) {
	// Don't bother filling in the image fields because they aren't used
	img := &image.Image{
		ImageV1: image.ImageV1{
			Architecture: "testarch",
			OS:           "testos",
		},
	}

	imgJSON, err := json.Marshal(img)
	if err != nil {
		t.Fatalf("json encoding failed: %v")
	}

	// To fill in rawJSON
	img, err = image.NewFromJSON(imgJSON)
	if err != nil {
		t.Fatalf("json decoding failed: %v")
	}

	fsLayers := []manifest.FSLayer{
		manifest.FSLayer{BlobSum: digest.Digest("sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4")},
		manifest.FSLayer{BlobSum: digest.Digest("sha256:86e0e091d0da6bde2456dbb48306f3956bbeb2eae1b5b9a43045843f69fe4aaa")},
		manifest.FSLayer{BlobSum: digest.Digest("sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4")},
	}

	manifest, err := CreateV2Manifest("testrepo", "testtag", img, fsLayers)
	if err != nil {
		t.Fatalf("CreateV2Manifest returned error: %v", err)
	}

	if manifest.Versioned.SchemaVersion != 1 {
		t.Fatal("SchemaVersion != 1")
	}
	if manifest.Name != "testrepo" {
		t.Fatal("incorrect name in manifest")
	}
	if manifest.Tag != "testtag" {
		t.Fatal("incorrect tag in manifest")
	}
	if manifest.Architecture != "testarch" {
		t.Fatal("incorrect arch in manifest")
	}
	if !reflect.DeepEqual(manifest.FSLayers, fsLayers) {
		t.Fatal("incorrect fsLayers list")
	}
	if len(manifest.History) != 3 {
		t.Fatal("wrong number of history entries")
	}

	expectedV1Compatibility := []string{
		`{"architecture":"testarch","container_config":{"Hostname":"","Domainname":"","User":"","AttachStdin":false,"AttachStdout":false,"AttachStderr":false,"Tty":false,"OpenStdin":false,"StdinOnce":false,"Env":null,"Cmd":null,"Image":"","Volumes":null,"WorkingDir":"","Entrypoint":null,"OnBuild":null,"Labels":null},"created":"0001-01-01T00:00:00Z","id":"f0cd5ca10b07f35512fc2f1cbf9a6cefbdb5cba70ac6b0c9e5988f4497f71937","os":"testos","parent":"9e3447ca24cb96d86ebd5960cb34d1299b07e0a0e03801d90b9969a2c187dd6e"}`,
		`{"id":"9e3447ca24cb96d86ebd5960cb34d1299b07e0a0e03801d90b9969a2c187dd6e","parent":"3690474eb5b4b26fdfbd89c6e159e8cc376ca76ef48032a30fa6aafd56337880"}`,
		`{"id":"3690474eb5b4b26fdfbd89c6e159e8cc376ca76ef48032a30fa6aafd56337880"}`,
	}

	for i := range expectedV1Compatibility {
		if manifest.History[i].V1Compatibility != expectedV1Compatibility[i] {
			t.Fatalf("wrong V1Compatibility %d. expected:\n%s\ngot:\n%s", i, expectedV1Compatibility[i], manifest.History[i].V1Compatibility)
		}
	}
}
