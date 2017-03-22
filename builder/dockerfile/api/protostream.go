package api

import (
	"encoding/binary"
	"io"
	"sync"

	"github.com/tonistiigi/fsutil"
)

var bufPool = sync.Pool{
	New: func() interface{} {
		return make([]byte, 32*1<<10)
	},
}

func NewProtoStream(r io.Reader, w io.Writer) fsutil.Stream {
	return &protoStream{r, w}
}

type protoStream struct {
	io.Reader
	io.Writer
}

func (c *protoStream) RecvMsg(m interface{}) (retErr error) {
	// logrus.Debugf(">RecvMsg")
	var length uint32
	// defer func() {
	//   logrus.Debugf("<RecvMsg: %v %v", retErr, length)
	// }()
	type unmarshaler interface {
		Unmarshal([]byte) error
	}
	var h [4]byte
	if _, err := io.ReadFull(c.Reader, h[:]); err != nil {
		return err
	}
	msg := m.(unmarshaler)
	length = binary.BigEndian.Uint32(h[:])
	if length == 0 {
		return nil
	}
	buf := bufPool.Get().([]byte)
	if cap(buf) < int(length) {
		buf = make([]byte, length)
	} else {
		buf = buf[:length]
	}
	defer bufPool.Put(buf)
	if _, err := io.ReadFull(c.Reader, buf); err != nil {
		return err
	}
	err := msg.Unmarshal(buf)
	if err != nil {
		return err
	}
	return nil
}

func (fc *protoStream) SendMsg(m interface{}) (retErr error) {
	// logrus.Debugf(">SendMsg")
	size := 0
	// defer func() {
	//   logrus.Debugf("<SendMsg: %v %v", retErr, size)
	// }()
	type marshalerSizer interface {
		MarshalTo([]byte) (int, error)
		Size() int
	}
	msg := m.(marshalerSizer)
	size = msg.Size()
	b := make([]byte, msg.Size()+4)
	binary.BigEndian.PutUint32(b[:4], uint32(size))
	if _, err := msg.MarshalTo(b[4:]); err != nil {
		return err
	}
	_, err := fc.Writer.Write(b)
	return err
}
