package libcontainerd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/Sirupsen/logrus"
)

var names = [3]string{"stdin", "stdout", "stderr"}

// Streams is an object for dealing with container input-output streams.
// FIXME: RemoteStreams?
type Streams interface {
	Attach(io.ReadCloser, io.Writer, io.Writer) error
	Cleanup() error
	FifoPath(index int) string
}

type streams struct {
	files    [3]*os.File
	base     string
	id       string
	attached bool
}

// GetStreams returns streams for a container ID.
func GetStreams(base, id string) (Streams, error) {
	for i := 0; i < 3; i++ {
		p := fifoname(base, id, i)
		fi, err := os.Lstat(p)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if err == nil {
			if fi.Mode()&os.ModeNamedPipe == 0 {
				if err := os.Remove(p); err != nil {
					return nil, err
				}
			} else {
				logrus.Debugf("reusing fifo: %s", p)
				continue
			}
		}
		if err := syscall.Mkfifo(p, 0700); err != nil {
			return nil, fmt.Errorf("mkfifo: %s %v", p, err)
		}
	}
	return &streams{base: base, id: id}, nil
}

func (s *streams) Attach(stdin io.ReadCloser, stdout, stderr io.Writer) error {
	if s.attached {
		return fmt.Errorf("already attached")
	}
	for i := 0; i < 3; i++ {
		switch i {
		case syscall.Stdin:
			go s.startInputStream(stdin, i)
		case syscall.Stdout:
			go s.startOutputStream(stdout, i)
		case syscall.Stderr:
			go s.startOutputStream(stderr, i)
		}

	}
	s.attached = true
	return nil
}

func (s *streams) startInputStream(source io.ReadCloser, i int) {
	p := fifoname(s.base, s.id, i)
	f, err := os.OpenFile(p, syscall.O_WRONLY, 0)
	s.files[i] = f
	if err != nil {
		logrus.Errorf("error opening: %s %v", p, err)
		source.Close()
		return
	}
	if source != nil {
		if _, err := io.Copy(f, source); err != nil {
			logrus.Errorf("error reading: %s %v", p, err)
		}
	}
	f.Close()
}

func (s *streams) startOutputStream(target io.Writer, i int) {
	p := fifoname(s.base, s.id, i)
	f, err := os.OpenFile(p, syscall.O_RDONLY, 0)
	s.files[i] = f
	if err != nil {
		logrus.Errorf("error opening: %s %v", p, err)
		return
	}
	if _, err := io.Copy(target, f); err != nil {
		logrus.Errorf("error reading: %s %v", p, err)
	}
	f.Close()
}

func (s *streams) Cleanup() error {
	for i := 0; i < 3; i++ {
		p := fifoname(s.base, s.id, i)
		if err := os.Remove(p); err != nil {
			return err
		}
	}
	return nil
}

func (s *streams) FifoPath(i int) string {
	return filepath.Join(s.base, s.id+"-"+names[i])
}

func fifoname(base, id string, i int) string {
	return filepath.Join(base, id+"-"+names[i])
}
