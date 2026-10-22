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

const DefaultBufferSize = 32 * 1024

var bufferPool32k = sync.Pool{
	New: func() interface{} {
		b := make([]byte, DefaultBufferSize)
		return &b
	},
}

// GetBuffer32k 从对象池获取 32KB 缓冲区，减少高频 make 分配与 GC 压力
func GetBuffer32k() *[]byte {
	return bufferPool32k.Get().(*[]byte)
}

// PutBuffer32k 归还 32KB 缓冲区到对象池
func PutBuffer32k(b *[]byte) {
	if b != nil && len(*b) == DefaultBufferSize {
		bufferPool32k.Put(b)
	}
}
