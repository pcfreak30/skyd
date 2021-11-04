package renter

import (
	"bytes"
	"sync"
)

var (
	staticPoolExecuteProgramBuffers = newExecuteProgramBufferPool()
	staticPoolUnresolvedWorkers     = newUnresolvedWorkersPool()
	staticPoolJobHasSectorResponse  = newJobHasSectorResponsePool()
	staticPoolIndividualWorkers     = newIndividualWorkerPool()
)

type (
	executeProgramBufferPool struct {
		staticPool sync.Pool
	}
	unresolvedWorkersPool struct {
		staticPool sync.Pool
	}
	jobHasSectorResponsePool struct {
		staticPool sync.Pool
	}
	individualWorkerPool struct {
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

func newJobHasSectorResponsePool() *jobHasSectorResponsePool {
	return &jobHasSectorResponsePool{
		staticPool: sync.Pool{
			New: func() interface{} {
				return &jobHasSectorResponse{}
			},
		},
	}
}

func (p *jobHasSectorResponsePool) Get() *jobHasSectorResponse {
	return p.staticPool.Get().(*jobHasSectorResponse)
}

func (p *jobHasSectorResponsePool) Put(iw *jobHasSectorResponse) {
	p.staticPool.Put(iw)
}

func newIndividualWorkerPool() *individualWorkerPool {
	return &individualWorkerPool{
		staticPool: sync.Pool{
			New: func() interface{} {
				return nil
			},
		},
	}
}

func (p *individualWorkerPool) Get() *[]individualWorker {
	i := p.staticPool.Get()
	if i == nil {
		return nil
	}
	iws := i.(*[]individualWorker)
	*iws = (*iws)[:cap(*iws)]
	return iws
}

func (p *individualWorkerPool) Put(iws *[]individualWorker) {
	p.staticPool.Put(iws)
}
