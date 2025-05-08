package events

import "sync"

// watermark tracks events dispatched in sequence order and completed in any
// order. mark is the highest sequence number such that it and every number
// dispatched before it have completed: the only safe value to persist as
// the consumer cursor, because everything up to it is applied.
type watermark struct {
	mu      sync.Mutex
	cond    *sync.Cond
	pending []int64 // dispatched, in order, not yet folded into mark
	done    map[int64]bool
	mark    int64
	limit   int // max events in flight (backpressure)
	closed  bool
}

func newWatermark(start int64, limit int) *watermark {
	w := &watermark{done: map[int64]bool{}, mark: start, limit: limit}
	w.cond = sync.NewCond(&w.mu)
	return w
}

// dispatch registers seq (strictly increasing) as in flight, blocking while
// limit events are already in flight. It returns false once closed.
func (w *watermark) dispatch(seq int64) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for len(w.pending) >= w.limit && !w.closed {
		w.cond.Wait()
	}
	if w.closed {
		return false
	}
	w.pending = append(w.pending, seq)
	return true
}

// complete marks seq applied and advances the mark over every finished
// prefix. It returns the (possibly unchanged) mark.
func (w *watermark) complete(seq int64) int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.done[seq] = true
	for len(w.pending) > 0 && w.done[w.pending[0]] {
		w.mark = w.pending[0]
		delete(w.done, w.pending[0])
		w.pending = w.pending[1:]
	}
	w.cond.Broadcast()
	return w.mark
}

func (w *watermark) current() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.mark
}

// close wakes blocked dispatchers.
func (w *watermark) close() {
	w.mu.Lock()
	w.closed = true
	w.cond.Broadcast()
	w.mu.Unlock()
}
