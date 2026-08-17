package registry

import (
	"fmt"
	"sync"
	"testing"
)

func TestBug4ConcurrentStatsIsRaceFree(t *testing.T) {
	r := New()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				r.Alloc(fmt.Sprintf("%d-%d", worker, j))
				_ = r.Stats()
			}
		}(i)
	}
	wg.Wait()
}
