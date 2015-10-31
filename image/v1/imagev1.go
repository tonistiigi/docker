package v1

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/Sirupsen/logrus"
	"github.com/docker/distribution/digest"
	"github.com/docker/docker/image"
	"github.com/docker/docker/layer"
	"github.com/docker/docker/pkg/version"
)

var validHex = regexp.MustCompile(`^([a-f0-9]{64})$`)

// noFallbackMinVersion is the minimum version for which v1compatibility
// information will not be marshaled through the Image struct to remove
// blank fields.
var noFallbackMinVersion = version.Version("1.8.3")

// HistoryFromConfig creates a History struct from v1 configuration JSON
func HistoryFromConfig(imageJSON []byte) (image.History, error) {
	h := image.History{}
	var v1Image image.V1Image
	if err := json.Unmarshal(imageJSON, &v1Image); err != nil {
		return h, err
	}

	h.Author = v1Image.Author
	h.Created = v1Image.Created
	h.CreatedBy = strings.Join(v1Image.ContainerConfig.Cmd.Slice(), " ")
	h.Comment = v1Image.Comment

	return h, nil
}

// CreateID creates an ID from v1 image, layerID and parent ID.
// Used for backwards compatibility with old clients.
func CreateID(v1Image image.V1Image, layerID layer.ID, parent digest.Digest) (digest.Digest, error) {
	v1Image.ID = ""
	v1JSON, err := json.Marshal(v1Image)
	if err != nil {
		return "", err
	}

	var config map[string]*json.RawMessage
	if err := json.Unmarshal(v1JSON, &config); err != nil {
		return "", err
	}

	// FIXME: note that this is slightly incompatible with RootFS logic
	config["layer_id"] = rawJSON(layerID)
	if parent != "" {
		config["parent"] = rawJSON(parent)
	}

	configJSON, err := json.Marshal(config)
	if err != nil {
		return "", err
	}
	logrus.Debugf("CreateV1ID %s", configJSON)

	return digest.FromBytes(configJSON)
}

// ConfigFromV1Config creates an image config from the legacy V1 config format.
func ConfigFromV1Config(imageJSON []byte, l layer.Layer, history []image.History) ([]byte, error) {

	var dver struct {
		DockerVersion string `json:"docker_version"`
	}

	useFallback := version.Version(dver.DockerVersion).LessThan(noFallbackMinVersion)

	if useFallback {
		var v1Image image.V1Image
		err := json.Unmarshal(imageJSON, &v1Image)
		if err != nil {
			return nil, err
		}
		imageJSON, err = json.Marshal(v1Image)
		if err != nil {
			return nil, err
		}
	}

	var c map[string]*json.RawMessage
	if err := json.Unmarshal(imageJSON, &c); err != nil {
		return nil, err
	}

	delete(c, "id")
	delete(c, "parent")
	delete(c, "Size") // Size is calculated from data on disk and is inconsitent
	delete(c, "parent_id")
	delete(c, "layer_id")

	layerDigests := addLayerDiffIDs(l)
	c["rootfs"] = rawJSON(&image.RootFS{Type: "layers", DiffIDs: layerDigests})
	c["history"] = rawJSON(history)

	return json.Marshal(c)
}

// V1ConfigFromConfig creates an legacy V1 image config from an Image struct
func V1ConfigFromConfig(img *image.Image, v1ID, parentV1ID string) ([]byte, error) {
	// Top-level v1compatibility string should be a modified version of the
	// image config.
	var configAsMap map[string]*json.RawMessage
	if err := json.Unmarshal(img.RawJSON(), &configAsMap); err != nil {
		return nil, err
	}

	// Delete fields that didn't exist in old manifest
	delete(configAsMap, "rootfs")
	delete(configAsMap, "history")
	configAsMap["id"] = rawJSON(v1ID)
	if parentV1ID != "" {
		configAsMap["parent"] = rawJSON(parentV1ID)
	}

	return json.Marshal(configAsMap)
}

func rawJSON(value interface{}) *json.RawMessage {
	jsonval, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return (*json.RawMessage)(&jsonval)
}

// ValidateID checks whether an ID string is a valid image ID.
func ValidateID(id string) error {
	if ok := validHex.MatchString(id); !ok {
		return fmt.Errorf("image ID '%s' is invalid ", id)
	}
	return nil
}

// addLayerDiffIDs creates a slice containing all layer diff IDs for the given
// layer, ordered from base to top-most.
func addLayerDiffIDs(l layer.Layer) []layer.DiffID {
	parent := l.Parent()
	if parent == nil {
		return []layer.DiffID{l.DiffID()}
	}
	return append(addLayerDiffIDs(parent), l.DiffID())
}
