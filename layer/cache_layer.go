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
	references     map[Layer]struct{}
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

func (cl *cacheLayer) Parent() Layer {
	if cl.parent == nil {
		return nil
	}
	return cl.parent
}

func (cl *cacheLayer) Size() (size int64, err error) {
	if cl.parent != nil {
		size, err = cl.parent.Size()
		if err != nil {
			return
		}
	}

	return size + cl.size, nil
}

func (cl *cacheLayer) DiffSize() (size int64, err error) {
	return cl.size, nil
}

func (cl *cacheLayer) Metadata() (map[string]string, error) {
	return cl.layerStore.driver.GetMetadata(cl.cacheID)
}

type referencedCacheLayer struct {
	*cacheLayer
}

func (cl *cacheLayer) getReference() Layer {
	ref := &referencedCacheLayer{
		cacheLayer: cl,
	}
	cl.references[ref] = struct{}{}

	return ref
}

func (cl *cacheLayer) hasReference(ref Layer) bool {
	_, ok := cl.references[ref]
	return ok
}

func (cl *cacheLayer) hasReferences() bool {
	return len(cl.references) > 0
}

func (cl *cacheLayer) deleteReference(ref Layer) {
	delete(cl.references, ref)
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
