package layers

import (
	"io/ioutil"
	"path/filepath"
	"testing"

	"github.com/docker/docker/daemon/graphdriver"
	"github.com/docker/docker/daemon/graphdriver/vfs"
	"github.com/docker/docker/pkg/archive"
)

func init() {
	graphdriver.ApplyUncompressedLayer = archive.UnpackLayer
	vfs.CopyWithTar = archive.CopyWithTar
}

func newTestGraphDriver(t *testing.T) graphdriver.Driver {
	td, err := ioutil.TempDir("", "graph")
	if err != nil {
		t.Fatal(err)
	}

	driver, err := graphdriver.GetDriver("vfs", td, nil)
	if err != nil {
		t.Fatal(err)
	}

	return driver
}

func TestMountAndRegister(t *testing.T) {
	ls, err := NewLayerStore("", newTestGraphDriver(t))
	if err != nil {
		t.Fatal(err)
	}

	mount, err := ls.Mount("test-mount", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	path, err := mount.Path()
	if err != nil {
		t.Fatal(err)
	}

	if err := ioutil.WriteFile(filepath.Join(path, "testfile.txt"), []byte("some test data"), 0644); err != nil {
		t.Fatal(err)
	}

	ts, err := mount.TarStream()
	if err != nil {
		t.Fatal(err)
	}

	layer, err := ls.Register(ts, "")
	if err != nil {
		t.Fatal(err)
	}

	size, _ := layer.Size()
	t.Logf("Layer size: %d", size)

	mount2, err := ls.Mount("new-test-mount", layer.ID(), "", nil)
	if err != nil {
		t.Fatal(err)
	}

	path2, err := mount2.Path()
	if err != nil {
		t.Fatal(err)
	}

	b, err := ioutil.ReadFile(filepath.Join(path2, "testfile.txt"))
	if err != nil {
		t.Fatal(err)
	}

	if expected := "some test data"; string(b) != expected {
		t.Fatalf("Wrong file data, expected %q, got %q", expected, string(b))
	}

	// TODO: unmount
}
