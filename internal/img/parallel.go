package img

import (
	"context"
	"runtime"
	"sync"
)

// Rows runs fn over [0,h) split into bands across the CPUs. It stops handing
// out bands once ctx is cancelled (ctx may be nil). Bands are small enough to
// balance uneven work and large enough to keep scheduling overhead low.
func Rows(ctx context.Context, h int, fn func(y0, y1 int)) {
	if h <= 0 {
		return
	}
	workers := runtime.GOMAXPROCS(0)
	band := h / (workers * 4)
	if band < 8 {
		band = 8
	}
	if workers == 1 || h <= band {
		fn(0, h)
		return
	}
	var next int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if ctx != nil && ctx.Err() != nil {
					return
				}
				mu.Lock()
				y0 := next
				next += band
				mu.Unlock()
				if y0 >= h {
					return
				}
				y1 := min(y0+band, h)
				fn(y0, y1)
			}
		}()
	}
	wg.Wait()
}

// Memory budget for working images. A 24-megapixel image is 192 MB in the
// working format and roughly twice that in linear float, so operations
// reserve what they will hold before allocating; on an 8 GB machine a batch
// then queues instead of swapping.
var budget = newSemaphore(1536 << 20)

// Reserve blocks until n bytes of working memory are available and returns
// the release function. Requests larger than the whole budget are clamped so
// a single huge image still runs (alone).
func Reserve(ctx context.Context, n int64) (func(), error) {
	return budget.acquire(ctx, n)
}

type semaphore struct {
	mu    sync.Mutex
	cond  *sync.Cond
	total int64
	used  int64
}

func newSemaphore(total int64) *semaphore {
	s := &semaphore{total: total}
	s.cond = sync.NewCond(&s.mu)
	return s
}

func (s *semaphore) acquire(ctx context.Context, n int64) (func(), error) {
	if n > s.total {
		n = s.total
	}
	if n < 0 {
		n = 0
	}
	s.mu.Lock()
	stop := context.AfterFunc(ctx, func() {
		s.mu.Lock()
		s.cond.Broadcast()
		s.mu.Unlock()
	})
	for s.used+n > s.total {
		if err := ctx.Err(); err != nil {
			s.mu.Unlock()
			stop()
			return nil, err
		}
		s.cond.Wait()
	}
	s.used += n
	s.mu.Unlock()
	stop()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.used -= n
			s.cond.Broadcast()
			s.mu.Unlock()
		})
	}, nil
}
