// Package prom runs instant PromQL queries against Grafana Cloud's
// Prometheus API with a read-only token.
package prom

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Raposa-Industries/adhunters/kit/ops"
)

// Client queries one Prometheus-compatible API.
type Client struct {
	// URL is the API base, the Grafana Cloud stack's Prometheus URL followed
	// by /api/prom (the /api/v1/query path is added here).
	URL      string
	User     string
	Token    string
	HTTP     *http.Client
	Location *time.Location
}

// Sample is one series of an instant vector.
type Sample struct {
	Labels map[string]string
	Value  float64
}

// Query evaluates q at t and returns the vector (a scalar comes back as one
// sample without labels).
func (c *Client) Query(ctx context.Context, q string, t time.Time) ([]Sample, error) {
	v := url.Values{"query": {q}, "time": {strconv.FormatInt(t.Unix(), 10)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+"/api/v1/query?"+v.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.User, c.Token)
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second, Transport: ops.Transport("grafana", nil)}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var r struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			ResultType string          `json:"resultType"`
			Result     json.RawMessage `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("prom: %s: %.200s", resp.Status, b)
	}
	if r.Status != "success" {
		return nil, fmt.Errorf("prom: %s: %s", resp.Status, r.Error)
	}
	switch r.Data.ResultType {
	case "vector":
		var vec []struct {
			Metric map[string]string `json:"metric"`
			Value  [2]any            `json:"value"`
		}
		if err := json.Unmarshal(r.Data.Result, &vec); err != nil {
			return nil, err
		}
		out := make([]Sample, 0, len(vec))
		for _, s := range vec {
			f, err := number(s.Value[1])
			if err != nil {
				return nil, err
			}
			out = append(out, Sample{Labels: s.Metric, Value: f})
		}
		return out, nil
	case "scalar":
		var sc [2]any
		if err := json.Unmarshal(r.Data.Result, &sc); err != nil {
			return nil, err
		}
		f, err := number(sc[1])
		if err != nil {
			return nil, err
		}
		return []Sample{{Value: f}}, nil
	}
	return nil, fmt.Errorf("prom: unexpected result type %q", r.Data.ResultType)
}

// One returns the single value of q at t, and false when there is no data.
func (c *Client) One(ctx context.Context, q string, t time.Time) (float64, bool, error) {
	s, err := c.Query(ctx, q, t)
	if err != nil || len(s) == 0 {
		return 0, false, err
	}
	return s[0].Value, true, nil
}

func number(v any) (float64, error) {
	s, ok := v.(string)
	if !ok {
		return 0, fmt.Errorf("prom: value %v is not a string", v)
	}
	return strconv.ParseFloat(s, 64)
}
