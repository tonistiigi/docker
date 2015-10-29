package layer

import "io"

type mountedLayer struct {
	name          string
	mountID       string
	initID        string
	parent        *cacheLayer
	path          string
	layerStore    *layerStore
	activityCount int
}

func (ml *mountedLayer) cacheParent() string {
	if ml.initID != "" {
		return ml.initID
	}
	if ml.parent != nil {
		return ml.parent.cacheID
	}
	return ""
}

func (ml *mountedLayer) TarStream() (io.Reader, error) {
	archiver, err := ml.layerStore.driver.Diff(ml.mountID, ml.cacheParent())
	if err != nil {
		return nil, err
	}
	r, w := io.Pipe()
	go func() {
		if _, err := io.Copy(w, archiver); err != nil {
			archiver.Close()
			w.CloseWithError(err)
			return
		}
		archiver.Close()
		w.Close()
	}()
	return r, nil
}

func (ml *mountedLayer) Path() (string, error) {
	if ml.path == "" {
		return "", ErrNotMounted
	}
	return ml.path, nil
}

func (ml *mountedLayer) Parent() Layer {
	return ml.parent
}

func (ml *mountedLayer) Size() (int64, error) {
	return ml.layerStore.driver.DiffSize(ml.mountID, ml.cacheParent())
}
