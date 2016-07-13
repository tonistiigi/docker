package ns

var (
	basePath string
)

// SetBasePath sets the base url prefix for the ns path
func SetBasePath(path string) {
	basePath = path
}

// BasePath returns the base url prefix for the ns path
func BasePath() string {
	return basePath
}
