// Package sentry reads new issues from Sentry's API with a read-only token.
// Sentry cannot post to Telegram itself, so observe-bot polls it.
package sentry

import (
	"context"
	"encoding/json"
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
}

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
// back at most 24 hours.
func (c *Client) NewSince(ctx context.Context, since time.Time) ([]Issue, error) {
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
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sentry: %s: %.200s", resp.Status, b)
	}
	var all []Issue
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, fmt.Errorf("sentry: %w", err)
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
