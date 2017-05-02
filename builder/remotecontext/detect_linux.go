package remotecontext

import (
	"io"
	"os"
)

func beforeClosing(file *os.File) (io.ReadCloser, error) {
	return file
}
