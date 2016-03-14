package libcontainerd

import (
	"io"

	"github.com/docker/docker/libcontainerd/windowsoci"
)

// process keeps the state for both main container process and exec process.

// process keeps the state for both main container process and exec process.
type process struct {
	processCommon

	// The ociProcess is required, as container.Start() in Windows
	// needs information that was originally passed in with the spec. This
	// avoids the start() function requiring a spec to be passed in
	// (and remembering the spec isn't available in the context of a restart
	// manager initiated start anyway).
	ociProcess windowsoci.Process
}

func openReaderFromPipe(p io.ReadCloser) io.Reader {
	r, w := io.Pipe()
	go func() {
		if _, err := io.Copy(w, p); err != nil {
			r.CloseWithError(err)
		}
		w.Close()
		p.Close()
	}()
	return r
}
