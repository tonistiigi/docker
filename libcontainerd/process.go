package libcontainerd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	containerd "github.com/docker/containerd/api/grpc/types"
	"github.com/docker/docker/pkg/ioutils"
	"golang.org/x/net/context"
)

var fdNames = map[int]string{
	syscall.Stdin:  "stdin",
	syscall.Stdout: "stdout",
	syscall.Stderr: "stderr",
}

// process keeps the state for both main container process and exec process.
type process struct {
	client    *client
	id        string // containerID
	processID string
	systemPid uint32
	dir       string
}

func (p *process) openFifos() (*IOPipe, error) {
	bundleDir := p.dir
	if err := os.MkdirAll(bundleDir, 0700); err != nil {
		return nil, err
	}

	for i := 0; i < 3; i++ {
		f := p.fifo(i)
		if err := syscall.Mkfifo(f, 0700); err != nil && !os.IsExist(err) {
			return nil, fmt.Errorf("mkfifo: %s %v", f, err)
		}
	}

	io := &IOPipe{}
	// FIXME: O_RDWR? open one-sided in goroutines?
	stdinf, err := os.OpenFile(p.fifo(syscall.Stdin), syscall.O_RDWR, 0)
	if err != nil {
		return nil, err
	}

	io.Stdout = openReaderFromFifo(p.fifo(syscall.Stdout))
	io.Stderr = openReaderFromFifo(p.fifo(syscall.Stderr))

	io.Stdin = ioutils.NewWriteCloserWrapper(stdinf, func() error {
		stdinf.Close()
		_, err := p.client.remote.apiClient.UpdateProcess(context.Background(), &containerd.UpdateProcessRequest{
			Id:         p.id,
			Pid:        p.processID,
			CloseStdin: true,
		})
		return err
	})

	return io, nil
}

func openReaderFromFifo(fn string) io.Reader {
	r, w := io.Pipe()
	go func() {
		stdoutf, err := os.OpenFile(fn, syscall.O_RDONLY, 0)
		if err != nil {
			r.CloseWithError(err)
		}
		if _, err := io.Copy(w, stdoutf); err != nil {
			r.CloseWithError(err)
		}
		w.Close()
	}()
	return r
}

func (p *process) fifo(index int) string {
	return filepath.Join(p.dir, p.processID+"-"+fdNames[index])
}
