package layer

import "io"

type cacheLayer struct {
	address    ID
	digest     DiffID
	parent     *cacheLayer
	cacheID    string
	size       int64
	layerStore *layerStore

	referenceCount int
}

func (cl *cacheLayer) TarStream() (io.Reader, error) {
	r, err := cl.layerStore.store.TarSplitReader(cl.address)
	if err != nil {
		return nil, err
	}

	return cl.layerStore.assembleTar(cl.cacheID, r)
}

func (cl *cacheLayer) ID() ID {
	return cl.address
}

func (cl *cacheLayer) DiffID() DiffID {
	return cl.digest
}

func (cl *cacheLayer) Parent() (Layer, error) {
	if cl.parent == nil {
		return nil, nil
	}
	return cl.parent, nil
}

func (cl *cacheLayer) Size() (int64, error) {
	return cl.size, nil
}

func (cl *cacheLayer) Metadata() (map[string]string, error) {
	return cl.layerStore.driver.GetMetadata(cl.cacheID)
}

func storeLayer(tx MetadataTransaction, layer *cacheLayer) error {
	if err := tx.SetDiffID(layer.digest); err != nil {
		return err
	}
	if err := tx.SetSize(layer.size); err != nil {
		return err
	}
	if err := tx.SetCacheID(layer.cacheID); err != nil {
		return err
	}
	if layer.parent != nil {
		if err := tx.SetParent(layer.parent.address); err != nil {
			return err
		}
	}

	return nil
}
