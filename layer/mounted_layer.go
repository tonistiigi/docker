package layer

import "io"

type mountedLayer struct {
	name          string
	mountID       string
	initID        string
	parent        *roLayer
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
	return autoClosingReader{archiver}, nil
}

func (ml *mountedLayer) Path() (string, error) {
	if ml.path == "" {
		return "", ErrNotMounted
	}
	return ml.path, nil
}

func (ml *mountedLayer) Parent() Layer {
	if ml.parent != nil {
		return ml.parent
	}

	// Return a nil interface instead of an interface wrapping a nil
	// pointer.
	return nil
}

func (ml *mountedLayer) Size() (int64, error) {
	return ml.layerStore.driver.DiffSize(ml.mountID, ml.cacheParent())
}

type autoClosingReader struct {
	source io.ReadCloser
}

func (r autoClosingReader) Read(p []byte) (n int, err error) {
	n, err = r.source.Read(p)
	if err != nil {
		r.source.Close()
	}
	return
}
