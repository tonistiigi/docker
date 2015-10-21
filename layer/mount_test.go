package layer

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
)

func TestMountInit(t *testing.T) {
	ls, cleanup := newTestStore(t)
	defer cleanup()

	basefile := newTestFile("testfile.txt", []byte("base data!"), 0644)
	initfile := newTestFile("testfile.txt", []byte("init data!"), 0777)

	li := initWithFiles(basefile)
	layer, err := createLayer(ls, "", li)
	if err != nil {
		t.Fatal(err)
	}

	mountInit := func(root string) error {
		return initfile.ApplyFile(root)
	}

	m, err := ls.Mount("fun-mount", layer.ID(), "", mountInit)
	if err != nil {
		t.Fatal(err)
	}

	path, err := m.Path()
	if err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(filepath.Join(path, "testfile.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}

	b, err := ioutil.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}

	if expected := "init data!"; string(b) != expected {
		t.Fatalf("Unexpected test file contents %q, expected %q", string(b), expected)
	}

	if fi.Mode().Perm() != 0777 {
		t.Fatalf("Unexpected filemode %o, expecting %o", fi.Mode().Perm(), 0777)
	}
}
