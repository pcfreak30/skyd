package renter

import (
	"bytes"
	"sync"
)

var (
	staticPoolExecuteProgramBuffers *executeProgramBufferPool
)

type (
	executeProgramBufferPool struct {
		staticPool sync.Pool
	}
)

func newExecuteProgramBufferPool() *executeProgramBufferPool {
	return &executeProgramBufferPool{
		staticPool: sync.Pool{
			New: func() interface{} {
				return bytes.NewBuffer(make([]byte, 1<<12))
			},
		},
	}
}

func (p *executeProgramBufferPool) Get() *bytes.Buffer {
	b := p.staticPool.Get().(*bytes.Buffer)
	b.Reset()
	return b
}

func (p *executeProgramBufferPool) Put(b *bytes.Buffer) {
	p.staticPool.Put(b)
}

func init() {
	staticPoolExecuteProgramBuffers = newExecuteProgramBufferPool()
}
