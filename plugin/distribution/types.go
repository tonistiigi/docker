package distribution

const (
	MediaTypeManifest = "application/vnd.docker.distribution.manifest.v2+json"
	MediaTypeConfig   = "application/vnd.docker.plugin.v0+json"
	MediaTypeLayer    = "application/vnd.docker.image.rootfs.diff.tar.gzip"
	DefaultTag        = "latest"
)
