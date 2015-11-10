package v1

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/docker/distribution/reference"
	"github.com/docker/docker/image"
	"github.com/docker/docker/layer"
)

func TestMigrateTags(t *testing.T) {
	tmpdir, err := ioutil.TempDir("", "migrate-tags")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpdir)

	ioutil.WriteFile(filepath.Join(tmpdir, "repositories-generic"), []byte(`{"Repositories":{"busybox":{"latest":"b3ca410aa2c115c05969a7b2c8cf8a9fcf62c1340ed6a601c9ee50df337ec108","sha256:16a2a52884c2a9481ed267c2d46483eac7693b813a63132368ab098a71303f8a":"b3ca410aa2c115c05969a7b2c8cf8a9fcf62c1340ed6a601c9ee50df337ec108"},"registry":{"2":"5d165b8e4b203685301c815e95663231691d383fd5e3d3185d1ce3f8dddead3d","latest":"8d5547a9f329b1d3f93198cd661fb5117e5a96b721c5cf9a2c389e7dd4877128"}}}`), 0600)

	ta := &mockTagAdder{}
	err = migrateTags(tmpdir, "generic", ta, map[string]image.ID{
		"5d165b8e4b203685301c815e95663231691d383fd5e3d3185d1ce3f8dddead3d": image.ID("sha256:2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae"),
		"b3ca410aa2c115c05969a7b2c8cf8a9fcf62c1340ed6a601c9ee50df337ec108": image.ID("sha256:fcde2b2edba56bf408601fb721fe9b5c338d10ee429ea04fae5511b68fbf8fb9"),
		"abcdef3434c115c05969a7b2c8cf8a9fcf62c1340ed6a601c9ee50df337ec108": image.ID("sha256:56434342345ae68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae"),
	})
	if err != nil {
		t.Fatal(err)
	}

	expected := map[string]string{
		"busybox:latest": "sha256:fcde2b2edba56bf408601fb721fe9b5c338d10ee429ea04fae5511b68fbf8fb9",
		"busybox@sha256:16a2a52884c2a9481ed267c2d46483eac7693b813a63132368ab098a71303f8a": "sha256:fcde2b2edba56bf408601fb721fe9b5c338d10ee429ea04fae5511b68fbf8fb9",
		"registry:2": "sha256:2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae",
	}

	if !reflect.DeepEqual(expected, ta.refs) {
		t.Fatal("Invalid migrated tags: expected %q, got %q", expected, ta.refs)
	}

	// test: second migration is noop
	// invalid data returns error
}

func TestMigrateContainers(t *testing.T) {
	tmpdir, err := ioutil.TempDir("", "migrate-containers")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpdir)

	err = addContainer(tmpdir, `{"State":{"Running":false,"Paused":false,"Restarting":false,"OOMKilled":false,"Dead":false,"Pid":0,"ExitCode":0,"Error":"","StartedAt":"2015-11-10T21:42:40.604267436Z","FinishedAt":"2015-11-10T21:42:41.869265487Z"},"ID":"f780ee3f80e66e9b432a57049597118a66aab8932be88e5628d4c824edbee37c","Created":"2015-11-10T21:42:40.433831551Z","Path":"sh","Args":[],"Config":{"Hostname":"f780ee3f80e6","Domainname":"","User":"","AttachStdin":true,"AttachStdout":true,"AttachStderr":true,"Tty":true,"OpenStdin":true,"StdinOnce":true,"Env":null,"Cmd":["sh"],"Image":"busybox","Volumes":null,"WorkingDir":"","Entrypoint":null,"OnBuild":null,"Labels":{}},"Image":"2c5ac3f849df8627fcf2822727f87c57f38b7129d3604fbc11d861fe856ff093","NetworkSettings":{"Bridge":"","EndpointID":"","Gateway":"","GlobalIPv6Address":"","GlobalIPv6PrefixLen":0,"HairpinMode":false,"IPAddress":"","IPPrefixLen":0,"IPv6Gateway":"","LinkLocalIPv6Address":"","LinkLocalIPv6PrefixLen":0,"MacAddress":"","NetworkID":"","PortMapping":null,"Ports":null,"SandboxKey":"","SecondaryIPAddresses":null,"SecondaryIPv6Addresses":null},"ResolvConfPath":"/var/lib/docker/containers/f780ee3f80e66e9b432a57049597118a66aab8932be88e5628d4c824edbee37c/resolv.conf","HostnamePath":"/var/lib/docker/containers/f780ee3f80e66e9b432a57049597118a66aab8932be88e5628d4c824edbee37c/hostname","HostsPath":"/var/lib/docker/containers/f780ee3f80e66e9b432a57049597118a66aab8932be88e5628d4c824edbee37c/hosts","LogPath":"/var/lib/docker/containers/f780ee3f80e66e9b432a57049597118a66aab8932be88e5628d4c824edbee37c/f780ee3f80e66e9b432a57049597118a66aab8932be88e5628d4c824edbee37c-json.log","Name":"/determined_euclid","Driver":"overlay","ExecDriver":"native-0.2","MountLabel":"","ProcessLabel":"","RestartCount":0,"UpdateDns":false,"HasBeenStartedBefore":false,"MountPoints":{},"Volumes":{},"VolumesRW":{},"AppArmorProfile":""}`)
	if err != nil {
		t.Fatal(err)
	}

	ls := &mockMounter{}

	ifs, err := image.NewFSStoreBackend(filepath.Join(tmpdir, "imagedb"))
	if err != nil {
		t.Fatal(err)
	}

	is, err := image.NewImageStore(ifs, ls)
	if err != nil {
		t.Fatal(err)
	}

	imgID, err := is.Create([]byte(`{"architecture":"amd64","config":{"AttachStdin":false,"AttachStdout":false,"AttachStderr":false,"Cmd":["sh"],"Entrypoint":null,"Env":null,"Hostname":"23304fc829f9","Image":"d1592a710ac323612bd786fa8ac20727c58d8a67847e5a65177c594f43919498","Labels":null,"OnBuild":null,"OpenStdin":false,"StdinOnce":false,"Tty":false,"Volumes":null,"WorkingDir":"","Domainname":"","User":""},"container":"349b014153779e30093d94f6df2a43c7a0a164e05aa207389917b540add39b51","container_config":{"AttachStdin":false,"AttachStdout":false,"AttachStderr":false,"Cmd":["/bin/sh","-c","#(nop) CMD [\"sh\"]"],"Entrypoint":null,"Env":null,"Hostname":"23304fc829f9","Image":"d1592a710ac323612bd786fa8ac20727c58d8a67847e5a65177c594f43919498","Labels":null,"OnBuild":null,"OpenStdin":false,"StdinOnce":false,"Tty":false,"Volumes":null,"WorkingDir":"","Domainname":"","User":""},"created":"2015-10-31T22:22:55.613815829Z","docker_version":"1.8.2","history":[{"created":"2015-10-31T22:22:54.690851953Z","created_by":"/bin/sh -c #(nop) ADD file:a3bc1e842b69636f9df5256c49c5374fb4eef1e281fe3f282c65fb853ee171c5 in /"},{"created":"2015-10-31T22:22:55.613815829Z","created_by":"/bin/sh -c #(nop) CMD [\"sh\"]"}],"os":"linux","rootfs":{"type":"layers","diff_ids":["sha256:c6f988f4874bb0add23a778f753c65efe992244e148a1d2ec2a8b664fb66bbd1","sha256:5f70bf18a086007016e948b04aed3b82103a36bea41755b6cddfaf10ace3c6ef"]}}`))
	if err != nil {
		t.Fatal(err)
	}

	err = migrateContainers(tmpdir, ls, is, map[string]image.ID{
		"2c5ac3f849df8627fcf2822727f87c57f38b7129d3604fbc11d861fe856ff093": imgID,
	})
	if err != nil {
		t.Fatal(err)
	}

	expected := []mountInfo{{
		"f780ee3f80e66e9b432a57049597118a66aab8932be88e5628d4c824edbee37c",
		"f780ee3f80e66e9b432a57049597118a66aab8932be88e5628d4c824edbee37c",
		"sha256:c3191d32a37d7159b2e30830937d2e30268ad6c375a773a8994911a3aba9b93f",
	}}
	if !reflect.DeepEqual(expected, ls.mounts) {
		t.Fatal("invalid mounts: expected %q, got %q", expected, ls.mounts)
	}

	if actual, expected := ls.count, 0; actual != expected {
		t.Fatal("invalid active mounts: expected %d, got %d", expected, actual)
	}

	config2, err := ioutil.ReadFile(filepath.Join(tmpdir, "containers", "f780ee3f80e66e9b432a57049597118a66aab8932be88e5628d4c824edbee37c", "config.v2.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct{ Image string }
	err = json.Unmarshal(config2, &config)
	if err != nil {
		t.Fatal(err)
	}

	if actual, expected := config.Image, string(imgID); actual != expected {
		t.Fatal("invalid image pointer in migrated config: expected %q, got %q", expected, actual)
	}

}

func addContainer(dest, jsonConfig string) error {
	var config struct{ ID string }
	if err := json.Unmarshal([]byte(jsonConfig), &config); err != nil {
		return err
	}
	contDir := filepath.Join(dest, "containers", config.ID)
	if err := os.MkdirAll(contDir, 0700); err != nil {
		return err
	}
	if err := ioutil.WriteFile(filepath.Join(contDir, "config.json"), []byte(jsonConfig), 0600); err != nil {
		return err
	}
	return nil
}

type mockTagAdder struct {
	refs map[string]string
}

func (t *mockTagAdder) Add(ref reference.Named, id image.ID, force bool) error {
	if t.refs == nil {
		t.refs = make(map[string]string)
	}
	t.refs[ref.String()] = id.String()
	return nil
}

// type mockImageCreateGetter struct {
// 	images  map[image.ID]*image.Image
// 	parents map[image.ID]image.Id
// }
//
// func (i *mockImageCreateGetter) Create(config []byte) (image.ID, error) {
//
// }
// func (i *mockImageCreateGetter) Get(id image.ID) (*image.Image, error) {
//
// }
// func (i *mockImageCreateGetter) SetParent(id image.ID, parent image.ID) error {
//
// }

type mockRegisterer struct {
}

func (r *mockRegisterer) RegisterByGraphID(graphID string, parent layer.ChainID, tarDataFile string) (layer.Layer, error) {
	return nil, nil
}
func (r *mockRegisterer) Release(l layer.Layer) ([]layer.Metadata, error) {
	return nil, nil
}

type mountInfo struct {
	name, graphID, parent string
}
type mockMounter struct {
	mounts []mountInfo
	count  int
}

func (r *mockMounter) MountByGraphID(name string, graphID string, parent layer.ChainID) (layer.RWLayer, error) {
	r.mounts = append(r.mounts, mountInfo{name, graphID, string(parent)})
	r.count += 1
	return nil, nil
}
func (r *mockMounter) Unmount(string) error {
	r.count -= 1
	return nil
}
func (r *mockMounter) Get(layer.ChainID) (layer.Layer, error) {
	return nil, nil
}

func (r *mockMounter) Release(layer.Layer) ([]layer.Metadata, error) {
	return nil, nil
}
