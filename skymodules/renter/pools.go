package renter

import (
	"bytes"
	"sync"
)

const (
	// executeProgrammBufferSize defines the size of the buffers distributed
	// by the executeProgram buffer pool
	//
	// NOTE: we empirically found 4kib to be a safe value
	executeProgramBufferSize = 1 << 12 // 4kib

	// providePaymentBufferSize defines the size of the buffers distributed
	// by the providePayment buffer pool
	//
	// NOTE: we empirically found 300 bytes to be a safe value
	providePaymentBufferSize = 300
)

var (
	// staticPoolExecuteProgramBuffers is the global pool for buffers used
	// by managedExecuteProgram.
	staticPoolExecuteProgramBuffers = newExecuteProgramBufferPool()

	// staticPoolProvidePaymentBuffers is the global pool for buffers used
	// by managedProvidePayment.
	staticPoolProvidePaymentBuffers = newProvidePaymentBufferPool()

	// staticPoolUnresolvedWorkers is the global pool for unresolvedWorker
	// objects.
	staticPoolUnresolvedWorkers = newUnresolvedWorkersPool()

	// staticPoolJobHasSectorResponse is the global pool for
	// jobHasSectorResponse objects.
	staticPoolJobHasSectorResponse = newJobHasSectorResponsePool()
)

type (
	// executeProgramBufferPool defines a pool for buffers to be used by
	// managedExecuteProgram.
	executeProgramBufferPool struct {
		staticPool sync.Pool
	}

	// providePaymentBufferPool defines a pool for buffers used by
	// managedProvidePayment.
	providePaymentBufferPool struct {
		staticPool sync.Pool
	}

	// unresolvedWorkersPool defines a pool of unresolvedWorker objects.
	unresolvedWorkersPool struct {
		staticPool sync.Pool
	}

	// jobHasSectorResponsePool defines a pool of jobHasSectorResponse objects.
	jobHasSectorResponsePool struct {
		staticPool sync.Pool
	}
)

// newUnresolvedWorkersPool creates a new unresolvedWorkersPool.
func newUnresolvedWorkersPool() *unresolvedWorkersPool {
	return &unresolvedWorkersPool{
		staticPool: sync.Pool{
			New: func() interface{} {
				return &pcwsUnresolvedWorker{}
			},
		},
	}
}

// Get returns an unresolvedWorker from the pool.
func (p *unresolvedWorkersPool) Get() *pcwsUnresolvedWorker {
	return p.staticPool.Get().(*pcwsUnresolvedWorker)
}

// Put returns an unresolvedWorker to the pool after we are done with it.
func (p *unresolvedWorkersPool) Put(w *pcwsUnresolvedWorker) {
	p.staticPool.Put(w)
}

// newExecuteProgramBufferPool creates a new executeProgramBufferPool.
func newExecuteProgramBufferPool() *executeProgramBufferPool {
	return &executeProgramBufferPool{
		staticPool: sync.Pool{
			New: func() interface{} {
				return bytes.NewBuffer(make([]byte, executeProgramBufferSize))
			},
		},
	}
}

// Get return a buffer from the pool and resets it beforehand.
func (p *executeProgramBufferPool) Get() *bytes.Buffer {
	b := p.staticPool.Get().(*bytes.Buffer)
	b.Reset()
	return b
}

// Put returns a buffer to the pool.
func (p *executeProgramBufferPool) Put(b *bytes.Buffer) {
	p.staticPool.Put(b)
}

// newJobHasSectorResponsePool create a new jobHasSectorResponsePool.
func newJobHasSectorResponsePool() *jobHasSectorResponsePool {
	return &jobHasSectorResponsePool{
		staticPool: sync.Pool{
			New: func() interface{} {
				return &jobHasSectorResponse{}
			},
		},
	}
}

// Get returns a jobHasSectorResponse from the pool.
func (p *jobHasSectorResponsePool) Get() *jobHasSectorResponse {
	return p.staticPool.Get().(*jobHasSectorResponse)
}

// Put returns a jobHasSectorResponse to the pool.
func (p *jobHasSectorResponsePool) Put(resp *jobHasSectorResponse) {
	p.staticPool.Put(resp)
}

// newProvidePaymentBufferPool creates a new providePaymentBufferPool.
func newProvidePaymentBufferPool() *providePaymentBufferPool {
	return &providePaymentBufferPool{
		staticPool: sync.Pool{
			New: func() interface{} {
				return bytes.NewBuffer(make([]byte, providePaymentBufferSize))
			},
		},
	}
}

// Get returns a buffer from the pool.
func (p *providePaymentBufferPool) Get() *bytes.Buffer {
	b := p.staticPool.Get().(*bytes.Buffer)
	b.Reset()
	return b
}

// Put returns a buffer to the pool.
func (p *providePaymentBufferPool) Put(b *bytes.Buffer) {
	p.staticPool.Put(b)
}
