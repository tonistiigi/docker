package distribution

import (
	"testing"

	"github.com/docker/docker/pkg/reexec"
)

func init() {
	reexec.Init()
}

func TestPools(t *testing.T) {
	p := NewPool()

	if _, found := p.add("pull", "test1"); found {
		t.Fatal("Expected pull test1 not to be in progress")
	}
	if _, found := p.add("pull", "test2"); found {
		t.Fatal("Expected pull test2 not to be in progress")
	}
	if _, found := p.add("push", "test1"); !found {
		t.Fatalf("Expected pull test1 to be in progress`")
	}
	if _, found := p.add("pull", "test1"); !found {
		t.Fatalf("Expected pull test1 to be in progress`")
	}
	if err := p.remove("pull", "test2"); err != nil {
		t.Fatal(err)
	}
	if err := p.remove("pull", "test2"); err != nil {
		t.Fatal(err)
	}
	if err := p.remove("pull", "test1"); err != nil {
		t.Fatal(err)
	}
	if err := p.remove("push", "test1"); err != nil {
		t.Fatal(err)
	}
}
