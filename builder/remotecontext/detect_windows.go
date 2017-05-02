package remotecontext

import (
	"bytes"
	"io"
	"os"
)

type bytesReaderCloser struct {
	*bytes.Reader
}

func (b bytesReaderCloser) Close() error {
	return nil
}
func beforeClosing(file *os.File) (io.ReadCloser, error) {
	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}
	b := make([]byte, stat.Size())

	_, err = file.Read(b)
	if err != nil {
		return nil, err
	}
	err = file.Close()
	if err != nil {
		return nil, err
	}
	return bytesReaderCloser{bytes.NewReader(b)}, nil
}
