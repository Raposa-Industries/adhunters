package collect

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/Raposa-Industries/adhunters/kit/ops"
)

// Job is one piece of collection run every Every. Delay puts off its first
// run after start, so a restart does not send every request at once.
type Job struct {
	Name  string
	Every time.Duration
	Delay time.Duration
	Run   func(ctx context.Context) error
}

// Schedule runs jobs until ctx ends, each on its own clock; a failed run is
// logged and reported (ops.Tasks), and the job runs again next time. It
// returns when every job has stopped.
func Schedule(ctx context.Context, log *slog.Logger, tasks *ops.Tasks, jobs []Job) {
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		go func(j Job) {
			defer wg.Done()
			if tasks != nil {
				// A task counts as late after two missed runs.
				tasks.Promise(j.Name, 2*j.Every+j.Delay)
			}
			if sleepCtx(ctx, j.Delay) != nil {
				return
			}
			for {
				start := time.Now()
				err := j.Run(ctx)
				if ctx.Err() != nil {
					return
				}
				if err != nil {
					log.Error("collect job failed", "job", j.Name, "err", err)
				}
				if tasks != nil {
					tasks.Done(j.Name, start, 0, err)
				}
				wait := j.Every - time.Since(start)
				if wait < time.Second {
					wait = time.Second
				}
				if sleepCtx(ctx, wait) != nil {
					return
				}
			}
		}(j)
	}
	wg.Wait()
}
