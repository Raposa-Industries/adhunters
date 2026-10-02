// Package engine is Raposa: it finds the dark funnel, the pages an operator
// shows real readers and hides from an ad network reviewer, by visiting the
// ad like a reader would, one disguise at a time.
//
// Ported from adhunters-collector e20148c (internal/raposa and its database
// code), with one change in how the work is held: an investigation moves one
// visit per claim, so any worker on any box resumes it, and a restart loses
// at most the visit in flight.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Raposa-Industries/adhunters/raposa/internal/lines"
	"github.com/Raposa-Industries/adhunters/shared/files"
	"github.com/Raposa-Industries/adhunters/shared/telegram"
)

// Config is what one engine needs.
type Config struct {
	Node        string // this box, as claims name it
	Workers     int    // visits at once
	BrowserAddr string
	// Telegram posts watched events to the ops group; nil: they are recorded
	// as skipped. BaseURL is where raposa-web's pages are, for the link.
	Telegram *telegram.Client
	BaseURL  string
	KeepDir  string // where the runner writes the files of one keep
}

// Engine runs investigations, keeps pages whole and delivers watches.
type Engine struct {
	cfg     Config
	log     *slog.Logger
	store   *Store
	lines   *lines.Set
	targets []Target
	files   files.Store
	fetcher *Fetcher
	browser *BrowserClient
	m       *metrics
}

// New builds an engine. reg may be nil.
func New(cfg Config, log *slog.Logger, store *Store, ls *lines.Set, targets []Target, fs files.Store, reg prometheus.Registerer) *Engine {
	if cfg.Workers < 1 {
		cfg.Workers = 1
	}
	return &Engine{
		cfg:     cfg,
		log:     log,
		store:   store,
		lines:   ls,
		targets: targets,
		files:   fs,
		fetcher: NewFetcher(),
		browser: NewBrowserClient(cfg.BrowserAddr),
		m:       newMetrics(reg),
	}
}

// Run works until ctx ends: the visit workers, the keeper, the notifier and
// the housekeeping (burned lines, the automatic quick queue).
func (e *Engine) Run(ctx context.Context) error {
	if n, err := e.store.ReleaseNode(ctx, e.cfg.Node); err != nil {
		return err
	} else if n > 0 {
		e.log.Info("resumed the investigations this node held when it stopped", "count", n)
	}
	if n, err := e.releaseKeeps(ctx); err != nil {
		return err
	} else if n > 0 {
		e.log.Info("put back the pages this node was keeping when it stopped", "count", n)
	}
	e.log.Info("raposa engine started", "node", e.cfg.Node, "workers", e.cfg.Workers)

	var wg sync.WaitGroup
	start := func(fn func(context.Context)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn(ctx)
		}()
	}
	for i := 0; i < e.cfg.Workers; i++ {
		start(e.work)
	}
	start(e.keep)
	start(e.notify)
	start(e.housekeep)
	wg.Wait()
	return ctx.Err()
}

// work claims one due investigation at a time and makes its next visit.
func (e *Engine) work(ctx context.Context) {
	for ctx.Err() == nil {
		inv, err := e.store.Claim(ctx, e.cfg.Node)
		if err != nil {
			if ctx.Err() == nil {
				e.log.Error("claim failed", "err", err)
			}
			sleep(ctx, 5*time.Second)
			continue
		}
		if inv == nil {
			sleep(ctx, 2*time.Second)
			continue
		}
		e.Step(ctx, inv)
	}
}

// Step makes one visit of one claimed investigation and writes it.
func (e *Engine) Step(ctx context.Context, inv *Investigation) {
	start := time.Now()
	result := e.step(ctx, inv)
	e.m.steps.WithLabelValues(result).Inc()
	e.m.stepSeconds.Observe(time.Since(start).Seconds())
}

func (e *Engine) step(ctx context.Context, inv *Investigation) string {
	before, _ := json.Marshal(inv.Progress)
	r, err := e.newRun(ctx, inv)
	if err == nil {
		err = r.advance(ctx)
	}
	if ctx.Err() != nil {
		// This node is stopping. Nothing of the visit in flight is written;
		// it runs again, here or elsewhere.
		if rerr := e.store.Release(context.WithoutCancel(ctx), inv, 0); rerr != nil {
			e.log.Error("release on stop", "investigation", inv.ID, "err", rerr)
		}
		return "stopped"
	}
	result := "visit"
	if w := asWait(err); w != nil {
		r.next = w.after
		r.ops = nil // a wait writes no visit
		err = nil
		result = "wait"
	}
	if err == nil {
		r.p.StepErrors = 0
		err = r.commit(ctx)
		if err == nil {
			for _, v := range r.visited {
				e.m.visits.WithLabelValues(v[0], v[1]).Inc()
			}
			if r.finished {
				result = "finished"
			}
			return result
		}
		if errors.Is(err, errLeaseLost) {
			e.log.Warn("the claim ran out before the visit was written; it runs again", "investigation", inv.ID)
			return "lease_lost"
		}
	}
	e.stepFailed(ctx, inv, before, err)
	return "error"
}

// newRun reads what a step needs besides the investigation itself.
func (e *Engine) newRun(ctx context.Context, inv *Investigation) (*run, error) {
	r := &run{e: e, inv: inv, p: &inv.Progress}
	var err error
	if r.set, err = e.store.LoadSettings(ctx); err != nil {
		return r, err
	}
	if r.ladder, err = e.store.LoadLadder(ctx); err != nil {
		return r, err
	}
	if inv.BurnScope != "" || inv.Progress.Phase != "" {
		if r.burned, err = e.store.BurnedLines(ctx, inv.BurnScope); err != nil {
			return r, err
		}
	}
	return r, nil
}

// stepFailed handles a step that failed for a reason that is not the page's
// (the database, a bug): the progress goes back to where the step found it,
// the investigation is tried again in 30 s, and after maxStepErrors failures
// in a row it fails with the last reason.
func (e *Engine) stepFailed(ctx context.Context, inv *Investigation, before []byte, cause error) {
	ctx = context.WithoutCancel(ctx)
	e.log.Error("step failed", "investigation", inv.ID, "err", cause)
	var p Progress
	if err := json.Unmarshal(before, &p); err != nil {
		p = Progress{}
	}
	p.StepErrors++
	inv.Progress = p
	r, err := e.newRun(ctx, inv)
	if err == nil {
		r.logf("a step failed and runs again: %v", cause)
		r.next = 30 * time.Second
		if p.StepErrors >= maxStepErrors {
			err = r.finish("failed", "the last "+itoa(maxStepErrors)+" steps failed: "+cause.Error())
		}
	}
	if err == nil {
		err = r.commit(ctx)
	}
	if err != nil {
		e.log.Error("could not record a failed step", "investigation", inv.ID, "err", err)
		_ = e.store.Release(ctx, inv, 30*time.Second)
	}
}

// housekeep rebuilds the burned lines, tops up the automatic quick queue
// and queues the follow-up runs that are due, every 5 minutes.
func (e *Engine) housekeep(ctx context.Context) {
	for {
		if n, err := e.store.RefreshLineBurns(ctx); err != nil {
			if ctx.Err() == nil {
				e.log.Error("refresh burned lines", "err", err)
			}
		} else {
			e.m.burns.Set(float64(n))
		}
		if n, err := e.store.QueueQuick(ctx); err != nil {
			if ctx.Err() == nil {
				e.log.Error("queue quick investigations", "err", err)
			}
		} else if n > 0 {
			e.m.autoQueued.Add(float64(n))
			e.log.Info("queued quick investigations for new ads", "count", n)
		}
		if n, err := e.store.ReleaseFollows(ctx); err != nil {
			if ctx.Err() == nil {
				e.log.Error("release follow-up runs", "err", err)
			}
		} else if n > 0 {
			e.log.Info("queued follow-up runs", "count", n)
		}
		if !sleep(ctx, 5*time.Minute) {
			return
		}
	}
}

// sleep waits d or until ctx ends, and says whether ctx is still going.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
