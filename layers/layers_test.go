package layers

import (
	"io/ioutil"
	"path/filepath"
	"testing"

	"github.com/docker/docker/daemon/graphdriver"
	_ "github.com/docker/docker/daemon/graphdriver/overlay"
	"github.com/docker/docker/pkg/reexec"
)

func init() {
	reexec.Init()
}

func TestMountAndRegister(t *testing.T) {
	td, err := ioutil.TempDir("", "graph")
	if err != nil {
		t.Fatal(err)
	}

	driver, err := graphdriver.GetDriver("overlay", td, nil)
	if err != nil {
		t.Fatal(err)
	}

	ls, err := NewLayerStore("", driver)
	if err != nil {
		t.Fatal(err)
	}

	mount, err := ls.Mount("test-mount", "")
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

	mount2, err := ls.Mount("new-test-mount", layer.ID())
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
