package proxy

import (
	"bytes"
	"sync"
)

type bufferPool struct {
	pool sync.Pool
}

var bytesBufferPool = &bufferPool{
	pool: sync.Pool{
		New: func() interface{} {
			return new(bytes.Buffer)
		},
	},
}

func (p *bufferPool) Get() *bytes.Buffer {
	buf := p.pool.Get().(*bytes.Buffer)
	buf.Reset()
	return buf
}

func (p *bufferPool) Put(buf *bytes.Buffer) {
	if buf.Cap() > 1024*1024 { // do not cache excessively large buffers
		return
	}
	buf.Reset()
	p.pool.Put(buf)
}
