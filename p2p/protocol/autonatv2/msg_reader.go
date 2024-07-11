package autonatv2

import (
	"io"

	"github.com/multiformats/go-varint"
)

// msgReader reads a varint prefixed message from R without any buffering
type msgReader struct {
	R   io.Reader
	Buf []byte
}

func (m *msgReader) ReadByte() (byte, error) {
	buf := m.Buf[:1]
	_, err := m.R.Read(buf)
	return buf[0], err
}

func (m *msgReader) ReadMsg() ([]byte, error) {
	sz, err := varint.ReadUvarint(m)
	if err != nil {
		return nil, err
	}
	if sz > uint64(len(m.Buf)) {
		return nil, io.ErrShortBuffer
	}
	n := 0
	for n < int(sz) {
		nr, err := m.R.Read(m.Buf[n:sz])
		if err != nil {
			return nil, err
		}
		n += nr
	}
	return m.Buf[:sz], nil
}
