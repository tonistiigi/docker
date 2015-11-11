package layer

import "errors"

// GetLayerPath returns the path to a layer
func GetLayerPath(s Store, layer ChainID) (string, error) {
	ls, ok := s.(*layerStore)
	if !ok {
		return "", errors.New("unsupported layer store")
	}
	ls.layerL.Lock()
	defer ls.layerL.Unlock()

	rl, ok := ls.layerMap[layer]
	if !ok {
		return "", ErrLayerDoesNotExist
	}

	path, err := ls.driver.Get(rl.cacheID, "")
	if err != nil {
		return "", err
	}

	if err := ls.driver.Put(rl.cacheID); err != nil {
		return "", err
	}

	return path, nil
}

// RWLayerMetadata returns the graph metadata for the provided
// mount name.
func RWLayerMetadata(s Store, name string) (map[string]string, error) {
	ls, ok := s.(*layerStore)
	if !ok {
		return nil, errors.New("unsupported layer store")
	}
	ls.mountL.Lock()
	defer ls.mountL.Unlock()

	ml, ok := ls.mounts[name]
	if !ok {
		return nil, errors.New("mount does not exist")
	}

	return ls.driver.GetMetadata(ml.mountID)
}
