package layer

import "io"

type mountedLayer struct {
	name       string
	mountID    string
	initID     string
	parent     *cacheLayer
	path       string
	layerStore *layerStore
}

func (ml *mountedLayer) TarStream() (io.Reader, error) {
	var pid string
	if ml.parent != nil {
		pid = ml.parent.cacheID
	}
	archiver, err := ml.layerStore.driver.Diff(ml.mountID, pid)
	return io.Reader(archiver), err
}

func (ml *mountedLayer) Path() (string, error) {
	if ml.path == "" {
		return "", ErrNotMounted
	}
	return ml.path, nil
}

func (ml *mountedLayer) Parent() (Layer, error) {
	return ml.parent, nil
}

func (ml *mountedLayer) Size() (int64, error) {
	pid := ml.initID
	if pid == "" && ml.parent != nil {
		pid = ml.parent.cacheID
	}
	return ml.layerStore.driver.DiffSize(ml.mountID, pid)
}
