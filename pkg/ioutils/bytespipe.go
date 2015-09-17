package ioutils

const maxCap = 1e6

// BytesPipe is io.ReadWriter which works similary to pipe(queue).
// All written data could be read only once. Also BytesPipe trying to adjust
// internal []byte slice to current needs, so there won't be overgrown buffer
// after highload peak.
// BytesPipe isn't goroutine-safe, caller must synchronize it if needed.
type BytesPipe struct {
	buf        [][]byte // slice of byte-slices of buffered data
	lastRead   int      // index in the first slice to a read point
	bufLen     int      // length of data buffered over the slices
	bufCap     int      // whole allocated capacity over slices
	writeIndex int      // index to the buffer where the writes go
}

// NewBytesPipe creates new BytesPipe, initialized by specified slice.
// If buf is nil, then it will be initialized with slice which cap is 64.
// buf will be adjusted in a way that len(buf) == 0, cap(buf) == cap(buf).
func NewBytesPipe(buf []byte) *BytesPipe {
	if cap(buf) == 0 {
		buf = make([]byte, 0, 64)
	}
	return &BytesPipe{
		buf:    [][]byte{buf[:0]},
		bufCap: cap(buf),
	}
}

func (bp *BytesPipe) grow(n int) {
	for bp.bufLen+n > bp.bufCap {
		// not enough space
		nextCap := 2 * cap(bp.buf[len(bp.buf)-1])
		if maxCap < nextCap {
			nextCap = maxCap
		}
		bp.buf = append(bp.buf, make([]byte, 0, nextCap))
		bp.bufCap += nextCap
	}
}

// Write writes p to BytesPipe.
// It can increase cap of internal []byte slice in a process of writing.
func (bp *BytesPipe) Write(p []byte) (n int, err error) {
	bp.grow(len(p))
	for {
		b := bp.buf[bp.writeIndex]
		n := copy(b[len(b):cap(b)], p)
		bp.bufLen += n
		bp.buf[bp.writeIndex] = b[:len(b)+n]
		if len(p) > n { // more data: write to the next slice
			p = p[n:]
			bp.writeIndex += 1
			continue
		}
		break
	}
	return
}

func (bp *BytesPipe) len() int {
	return bp.bufLen - bp.lastRead
}

// Read reads bytes from BytesPipe.
// Data could be read only once.
func (bp *BytesPipe) Read(p []byte) (n int, err error) {
	for {
		written := copy(p, bp.buf[0][bp.lastRead:])
		n += written
		bp.lastRead += written
		if len(p) > written && bp.len() > 0 {
			// more buffered data and more asked. read from next slice.
			p = p[written:]
			bp.lastRead = 0
			bp.bufLen -= len(bp.buf[0])
			bp.bufCap -= len(bp.buf[0])
			bp.buf[0] = nil     // throw away old slice
			bp.buf = bp.buf[1:] // switch to next
			bp.writeIndex -= 1
			continue
		}
		if bp.len() == 0 {
			// we have read everything. reset to the beginning.
			bp.lastRead = 0
			bp.bufLen -= len(bp.buf[0])
			bp.buf[0] = bp.buf[0][:0]
		}
		break
	}
	return
}
