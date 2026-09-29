package openai

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// A vendor that answers "too fast" is not a failure worth showing. OpenAI
// rate-limits image generation by the minute, and a page that asks for six
// pictures at once sends more calls than one window allows, so the tail of a
// batch comes back 429. That answer clears on its own.
//
// retry wraps exactly one call and stops at the first error that is not
// marked transient. Only an error reply (429 or 5xx) is transient: those
// billed nothing, so a repeat can never pay twice for one picture.
const (
	// attempts is one try plus two repeats.
	attempts = 3
	// retryBase is the first pause when the vendor sent no Retry-After; it
	// doubles per attempt.
	retryBase = 2 * time.Second
	// retryMax caps any pause. OpenAI's window is a minute.
	retryMax = 60 * time.Second
)

// retry runs fn until it succeeds, returns an error that is not transient,
// or runs out of attempts.
func retry(ctx context.Context, base time.Duration, fn func() ([]byte, error)) ([]byte, error) {
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 && !sleepContext(ctx, retryDelay(lastErr, base, attempt-1)) {
			// The caller gave up while we were waiting; lastErr is the reason.
			return nil, lastErr
		}
		out, err := fn()
		if err == nil {
			return out, nil
		}
		lastErr = err
		if !isTransient(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

func isTransient(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.transient
}

// retryableStatus is the codes that mean "too fast" or "our bad moment".
func retryableStatus(status int) bool {
	return status == 429 || (status >= 500 && status <= 599)
}

// retryDelay is the vendor's Retry-After when there was one, clamped to
// retryMax, otherwise a doubling backoff from base.
func retryDelay(err error, base time.Duration, attempt int) time.Duration {
	var e *Error
	if !errors.As(err, &e) {
		return 0
	}
	if e.retryAfter > 0 {
		return min(e.retryAfter, retryMax)
	}
	if base <= 0 {
		return 0
	}
	return min(base<<(attempt-1), retryMax)
}

// sleepContext waits out a backoff and reports whether the wait finished.
func sleepContext(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// retryAfterHeader reads OpenAI's retry-after-ms when present, else
// Retry-After in either shape: seconds or an HTTP date.
func retryAfterHeader(res *http.Response) time.Duration {
	if ms, err := strconv.Atoi(strings.TrimSpace(res.Header.Get("Retry-After-Ms"))); err == nil && ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	h := strings.TrimSpace(res.Header.Get("Retry-After"))
	if h == "" {
		return 0
	}
	if secs, err := strconv.Atoi(h); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}
