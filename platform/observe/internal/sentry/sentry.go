// Package sentry reads new issues from Sentry's API with a read-only token.
// Sentry cannot post to Telegram itself, so observe-bot polls it.
package sentry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"
)

// Client reads one project's issues.
type Client struct {
	// URL is https://sentry.io, or https://de.sentry.io for an EU organization.
	URL     string
	Org     string
	Project string
	Token   string
	HTTP    *http.Client
	// Backoff is the wait before the first retry; it doubles each time.
	// Zero means 2 seconds.
	Backoff time.Duration
}

// tries is how many times NewSince asks before it gives up on a timeout, a
// dropped connection, a 429 or a 5xx. Sentry's API has short slow spells.
const tries = 3

// Issue is one Sentry issue: one kind of error, however often it happened.
type Issue struct {
	ID        string    `json:"id"`
	ShortID   string    `json:"shortId"`
	Title     string    `json:"title"`
	Culprit   string    `json:"culprit"`
	Permalink string    `json:"permalink"`
	FirstSeen time.Time `json:"firstSeen"`
	Count     int64     `json:"-"`
	RawCount  string    `json:"count"`
}

// NewSince returns the issues first seen after since, oldest first. It looks
// back at most 24 hours. A transient failure is tried again, up to three
// times in all.
func (c *Client) NewSince(ctx context.Context, since time.Time) ([]Issue, error) {
	wait := c.Backoff
	if wait <= 0 {
		wait = 2 * time.Second
	}
	var all []Issue
	var err error
	for try := 1; ; try++ {
		all, err = c.issues(ctx)
		var tr transient
		if err == nil || try == tries || !errors.As(err, &tr) {
			break
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(wait):
		}
		wait *= 2
	}
	if err != nil {
		return nil, err
	}
	var out []Issue
	for _, is := range all {
		if !is.FirstSeen.After(since) {
			continue
		}
		is.Count, _ = strconv.ParseInt(is.RawCount, 10, 64)
		out = append(out, is)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FirstSeen.Before(out[j].FirstSeen) })
	return out, nil
}

// transient marks a failure worth asking again: no answer, or Sentry saying
// it is busy or broken. A 401 or 404 is a setting and never retried.
type transient struct{ err error }

func (t transient) Error() string { return t.err.Error() }
func (t transient) Unwrap() error { return t.err }

// issues asks once for the project's issues of the last 24 hours.
func (c *Client) issues(ctx context.Context) ([]Issue, error) {
	q := url.Values{
		"query":       {"firstSeen:-24h"},
		"sort":        {"new"},
		"limit":       {"100"},
		"statsPeriod": {""},
	}
	u := fmt.Sprintf("%s/api/0/projects/%s/%s/issues/?%s", c.URL, url.PathEscape(c.Org), url.PathEscape(c.Project), q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, err
		}
		return nil, transient{err}
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, transient{err}
	}
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("sentry: %s: %.200s", resp.Status, b)
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return nil, transient{err}
		}
		return nil, err
	}
	var all []Issue
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, fmt.Errorf("sentry: %w", err)
	}
	return all, nil
}
