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

type Streams interface {
	Attach(io.ReadCloser, io.Writer, io.Writer) error
	Cleanup() error
	FifoPath(index int) string
}

type streams struct {
	// waitopen sync.WaitGroup
	// waitdone sync.WaitGroup
	files    [3]*os.File
	base     string
	id       string
	attached bool
}

func GetStreams(base, id string) (Streams, error) {
	for i := 0; i < 3; i++ {
		p := fifoname(base, id, i)
		fi, err := os.Lstat(p)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if err == nil {
			if fi.Mode()&os.ModeNamedPipe != 0 {
				if err := os.Remove(p); err != nil {
					return nil, err
				}
			} else {
				logrus.Debugf("reusing", p)
				continue
			}
		}
		if err := syscall.Mkfifo(p, 0700); err != nil {
			return nil, fmt.Errorf("mkfifo: %s %v", p, err)
		}
	}
	logrus.Debugf("clean return")
	return &streams{base: base, id: id}, nil
}

func (s *streams) Attach(stdin io.ReadCloser, stdout, stderr io.Writer) error {
	if s.attached {
		return fmt.Errorf("already attached")
	}
	for i := 0; i < 3; i++ {
		switch i {
		case 0:
			go s.startInputStream(stdin, i)
		case 1:
			go s.startOutputStream(stdout, i)
		case 2:
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

//
// func (s *Streams) Detach() {
//
// }

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
