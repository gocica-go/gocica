package locker_test

import (
	"sync"
	"testing"
	"time"

	isulocker "github.com/mazrean/gocica/internal/pkg/locker"
)

func TestAfterFirst(t *testing.T) {
	af := isulocker.NewWaitSuccess()

	sLocker := sync.Mutex{}
	s := make([]bool, 0, 5)
	wg := sync.WaitGroup{}
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			af.Run(func() bool {
				func() {
					sLocker.Lock()
					defer sLocker.Unlock()
					s = append(s, true)
				}()
				time.Sleep(1 * time.Second)

				return true
			}, func() {
				sLocker.Lock()
				defer sLocker.Unlock()
				s = append(s, false)
			})
		}()
	}
	wg.Wait()

	if len(s) != 5 {
		t.Errorf("s.Len() = %d, want %d", len(s), 5)
	}
	v := s[0]
	if !v {
		t.Errorf("s[0] = %t, want %t", v, true)
	}

	for i := 1; i < 5; i++ {
		v := s[i]
		if v {
			t.Errorf("s[%d] = %t, want %t", i, v, false)
		}
	}

	af.Run(func() bool {
		sLocker.Lock()
		defer sLocker.Unlock()
		s = append(s, true)

		return true
	}, func() {
		sLocker.Lock()
		defer sLocker.Unlock()
		s = append(s, false)
	})
	v = s[5]
	if v {
		t.Errorf("s[5] = %t, want %t", v, true)
	}
}
