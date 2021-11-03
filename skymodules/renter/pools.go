package renter

import (
	"bytes"
	"sync"
)

var (
	staticPoolExecuteProgramBuffers = newExecuteProgramBufferPool()
	staticPoolUnresolvedWorkers     = newUnresolvedWorkersPool()
	staticPoolIndividualWorkers     = newIndividualWorkerPool()
)

type (
	executeProgramBufferPool struct {
		staticPool sync.Pool
	}
	individualWorkerPool struct {
		staticPool sync.Pool
	}
	unresolvedWorkersPool struct {
		staticPool sync.Pool
	}
)

func newUnresolvedWorkersPool() *unresolvedWorkersPool {
	return &unresolvedWorkersPool{
		staticPool: sync.Pool{
			New: func() interface{} {
				return &pcwsUnresolvedWorker{}
			},
		},
	}
}

func (p *unresolvedWorkersPool) Get() *pcwsUnresolvedWorker {
	return p.staticPool.Get().(*pcwsUnresolvedWorker)
}

func (p *unresolvedWorkersPool) Put(w *pcwsUnresolvedWorker) {
	p.staticPool.Put(w)
}

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

func newIndividualWorkerPool() *individualWorkerPool {
	return &individualWorkerPool{
		staticPool: sync.Pool{
			New: func() interface{} {
				return &individualWorker{}
			},
		},
	}
}

func (p *individualWorkerPool) Get() *individualWorker {
	return p.staticPool.Get().(*individualWorker)
}

func (p *individualWorkerPool) Put(iw *individualWorker) {
	p.staticPool.Put(iw)
}
