package alborz

import (
	"context"
	"sync"
	"time"
)

// Schedule runs the work that keeps its own time until the server closes.
type Schedule struct {
	ctx  context.Context
	stop context.CancelFunc
	jobs sync.WaitGroup
}

func newSchedule() *Schedule {
	ctx, stop := context.WithCancel(context.Background())
	return &Schedule{ctx: ctx, stop: stop}
}

// Add runs job every interval, given the tick's time. A run longer than the interval skips the ticks it overlaps.
func (s *Schedule) Add(every time.Duration, job func(context.Context, time.Time)) {
	s.jobs.Go(func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case now := <-t.C:
				job(s.ctx, now)
			}
		}
	})
}

func (s *Schedule) close() {
	s.stop()
	s.jobs.Wait()
}
