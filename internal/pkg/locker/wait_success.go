package locker

import (
	"sync"
	"sync/atomic"
)

type WaitSuccess struct {
	// isRunning
	// Whether the process is currently running
	isRunning *atomic.Bool
	// succeeded
	// Indicates if the process has ever succeeded
	// Can only change from false to true (one-way flag)
	succeeded *atomic.Bool
	cond      *sync.Cond
}

func NewWaitSuccess() *WaitSuccess {
	isRunning := &atomic.Bool{}
	isRunning.Store(false)

	succeeded := &atomic.Bool{}
	succeeded.Store(false)

	return &WaitSuccess{
		isRunning: isRunning,
		succeeded: succeeded,
		cond:      sync.NewCond(&sync.Mutex{}),
	}
}

func (af *WaitSuccess) Run(before func() bool, after func()) {
	func() {
		af.cond.L.Lock()
		defer af.cond.L.Unlock()

		if af.isRunning.Load() && !af.succeeded.Load() {
			af.cond.Wait()
		}
		af.isRunning.Store(true)
	}()

	if af.succeeded.Load() {
		after()
	} else {
		result := before()

		af.cond.L.Lock()
		defer af.cond.L.Unlock()

		af.succeeded.Store(result)
		af.isRunning.Store(false)
	}

	if af.succeeded.Load() {
		af.cond.Broadcast()
	} else {
		af.cond.Signal()
	}
}
