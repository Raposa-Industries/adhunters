package collect

import (
	"context"
	"sync"
	"time"
)

// Pacer spaces one Taboola login's requests so Intel stays inside its share
// of Taboola's limits: 84 standard requests a minute and 10 realtime ones,
// per login, shared with Launch. Until the budget shared through the
// database exists, Intel keeps to PerMinute and RealtimePerMinute on its own.
type Pacer struct {
	PerMinute         int
	RealtimePerMinute int

	mu       sync.Mutex
	next     time.Time
	nextRT   time.Time
	sleep    func(context.Context, time.Duration) error
	now      func() time.Time
	disabled bool // tests
}

// NewPacer returns a pacer at Intel's default share: 40 standard and 8
// realtime requests a minute.
func NewPacer() *Pacer { return &Pacer{PerMinute: 40, RealtimePerMinute: 8} }

// Wait blocks until the next request of its kind may go.
func (p *Pacer) Wait(ctx context.Context, realtime bool) error {
	if p == nil || p.disabled {
		return ctx.Err()
	}
	now := time.Now
	if p.now != nil {
		now = p.now
	}
	p.mu.Lock()
	t := now()
	gap := time.Minute / time.Duration(max(p.PerMinute, 1))
	slot := &p.next
	if realtime {
		gap = time.Minute / time.Duration(max(p.RealtimePerMinute, 1))
		slot = &p.nextRT
	}
	at := *slot
	// A realtime request is a request too: it takes a standard slot as well.
	if realtime && at.Before(p.next) {
		at = p.next
	}
	if at.Before(t) {
		at = t
	}
	*slot = at.Add(gap)
	if realtime {
		p.next = at.Add(time.Minute / time.Duration(max(p.PerMinute, 1)))
	}
	p.mu.Unlock()
	d := at.Sub(t)
	if d <= 0 {
		return ctx.Err()
	}
	sleep := p.sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	return sleep(ctx, d)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
