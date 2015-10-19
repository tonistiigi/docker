package daemon

import (
	"fmt"
	"sort"
	"strings"

	"github.com/docker/distribution/reference"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/images"
	"github.com/docker/docker/pkg/parsers/filters"
)

var acceptedImageFilterTags = map[string]struct{}{
	"dangling": {},
	"label":    {},
}

// byCreated is a temporary type used to sort a list of images by creation
// time.
type byCreated []*types.Image

func (r byCreated) Len() int           { return len(r) }
func (r byCreated) Swap(i, j int)      { r[i], r[j] = r[j], r[i] }
func (r byCreated) Less(i, j int) bool { return r[i].Created < r[j].Created }

// Images returns a filtered list of images. filterArgs is a JSON-encoded set
// of filter arguments which will be interpreted by pkg/parsers/filters.
// filter is a shell glob string applied to repository names. The argument
// named all controls whether all images in the graph are filtered, or just
// the heads.
func (daemon *Daemon) Images(filterArgs, filter string, all bool) ([]*types.Image, error) {
	var (
		allImages    map[images.ID]*images.Image
		err          error
		danglingOnly = false
	)

	imageFilters, err := filters.FromParam(filterArgs)
	if err != nil {
		return nil, err
	}
	for name := range imageFilters {
		if _, ok := acceptedImageFilterTags[name]; !ok {
			return nil, fmt.Errorf("Invalid filter '%s'", name)
		}
	}

	if i, ok := imageFilters["dangling"]; ok {
		for _, value := range i {
			if strings.ToLower(value) == "true" {
				danglingOnly = true
			}
		}
	}

	if all && !danglingOnly {
		allImages = daemon.imageStore.Map()
	} else {
		allImages = daemon.imageStore.Heads()
	}

	images := []*types.Image{}

	var filterTagged bool
	if filter != "" {
		filterRef, err := reference.Parse(filter)
		if err != nil {
			return nil, err
		}
		if _, ok := filterRef.(reference.Tagged); ok {
			filterTagged = true
		}
	}

	for id, img := range allImages {
		if _, ok := imageFilters["label"]; ok {
			if img.Config == nil {
				// Very old image that do not have image.Config (or even labels)
				continue
			}
			// We are now sure image.Config is not nil
			if !imageFilters.MatchKVList("label", img.Config.Labels) {
				continue
			}
		}

		newImage := newImage(img, 0) // FIXME: parentSize

		for _, ref := range daemon.tagStore.References(id) {
			if filter != "" { // filter by tag/repo name
				if filterTagged { // filter by tag, require full ref match
					if ref.String() != filter {
						continue
					}
				} else if ref.Name() != filter { // name only match
					continue
				}
			}
			if _, ok := ref.(reference.Digested); ok {
				newImage.RepoDigests = append(newImage.RepoDigests, ref.String())
			}
			if _, ok := ref.(reference.Tagged); ok {
				newImage.RepoTags = append(newImage.RepoTags, ref.String())
			}
		}
		if newImage.RepoDigests == nil && newImage.RepoTags == nil {
			if filter != "" { // skip images with no references if filtering by tag
				continue
			}
			newImage.RepoDigests = []string{"<none>@<none>"}
			newImage.RepoTags = []string{"<none>:<none>"}
		} else if danglingOnly {
			continue
		}

		images = append(images, newImage)
	}

	sort.Sort(sort.Reverse(byCreated(images)))

	return images, nil
}

func newImage(image *images.Image, parentSize int64) *types.Image {
	newImage := new(types.Image)
	newImage.ParentID = image.Parent
	newImage.ID = image.ID.String()
	newImage.Created = image.Created.Unix()
	newImage.Size = image.Size
	newImage.VirtualSize = parentSize + image.Size
	if image.Config != nil {
		newImage.Labels = image.Config.Labels
	}
	return newImage
}
