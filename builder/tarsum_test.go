package builder

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/docker/docker/pkg/archive"
	"github.com/docker/docker/pkg/reexec"
)

const (
	filename = "test"
	contents = "contents test"
)

func init() {
	reexec.Init()
}

func TestCloseRootDirectory(t *testing.T) {
	contextDir, err := ioutil.TempDir("", "builder-tarsum-test")

	if err != nil {
		t.Fatalf("Error with creating temporary directory: %s", err)
	}

	tarsum := &tarSumContext{root: contextDir}

	err = tarsum.Close()

	if err != nil {
		t.Fatalf("Error while executing Close: %s", err)
	}

	_, err = os.Stat(contextDir)

	if !os.IsNotExist(err) {
		t.Fatal("Directory should not exist at this point")
		defer os.RemoveAll(contextDir)
	}
}

func TestStatFile(t *testing.T) {
	contextDir, cleanup := createTestTempDir(t, "", "builder-tarsum-test")
	defer cleanup()

	createTestTempFile(t, contextDir, filename, contents, 0777)

	tarSum := &tarSumContext{root: contextDir}

	sum, err := tarSum.Hash(filename)

	if err != nil {
		t.Fatalf("Error when executing Stat: %s", err)
	}

	if len(sum) == 0 {
		t.Fatalf("Hash returned empty sum")
	}
}

func TestStatSubdir(t *testing.T) {
	contextDir, cleanup := createTestTempDir(t, "", "builder-tarsum-test")
	defer cleanup()

	contextSubdir := createTestTempSubdir(t, contextDir, "builder-tarsum-test-subdir")

	testFilename := createTestTempFile(t, contextSubdir, filename, contents, 0777)

	tarSum := &tarSumContext{root: contextDir}

	relativePath, err := filepath.Rel(contextDir, testFilename)

	if err != nil {
		t.Fatalf("Error when getting relative path: %s", err)
	}

	sum, err := tarSum.Hash(relativePath)

	if err != nil {
		t.Fatalf("Error when executing Stat: %s", err)
	}

	if len(sum) == 0 {
		t.Fatalf("Hash returned empty sum")
	}
}

func TestStatNotExisting(t *testing.T) {
	contextDir, cleanup := createTestTempDir(t, "", "builder-tarsum-test")
	defer cleanup()

	tarSum := &tarSumContext{root: contextDir}

	_, err := tarSum.Hash("not-existing")

	if !os.IsNotExist(err) {
		t.Fatalf("This file should not exist: %s", err)
	}
}

func TestRemoveDirectory(t *testing.T) {
	contextDir, cleanup := createTestTempDir(t, "", "builder-tarsum-test")
	defer cleanup()

	contextSubdir := createTestTempSubdir(t, contextDir, "builder-tarsum-test-subdir")

	relativePath, err := filepath.Rel(contextDir, contextSubdir)

	if err != nil {
		t.Fatalf("Error when getting relative path: %s", err)
	}

	tarSum := &tarSumContext{root: contextDir}

	err = tarSum.Remove(relativePath)

	if err != nil {
		t.Fatalf("Error when executing Remove: %s", err)
	}

	_, err = os.Stat(contextSubdir)

	if !os.IsNotExist(err) {
		t.Fatal("Directory should not exist at this point")
	}
}

func TestMakeTarSumContext(t *testing.T) {
	contextDir, cleanup := createTestTempDir(t, "", "builder-tarsum-test")
	defer cleanup()

	createTestTempFile(t, contextDir, filename, contents, 0777)

	tarStream, err := archive.Tar(contextDir, archive.Uncompressed)

	if err != nil {
		t.Fatalf("error: %s", err)
	}

	defer tarStream.Close()

	tarSum, err := MakeTarSumContext(tarStream)

	if err != nil {
		t.Fatalf("Error when executing MakeTarSumContext: %s", err)
	}

	if tarSum == nil {
		t.Fatal("Tar sum context should not be nil")
	}
}
