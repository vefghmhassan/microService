package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// restClient is the small JSON-over-HTTP client shared by the bearer-token
// providers. BaseURL is a field so tests can point it at httptest servers.
type restClient struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func newREST(base, token string) *restClient {
	return &restClient{BaseURL: base, Token: token, HTTP: &http.Client{Timeout: 60 * time.Second}}
}

// APIError carries the provider's status and body so callers can see why.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	body := strings.TrimSpace(e.Body)
	if len(body) > 400 {
		body = body[:400] + "…"
	}
	return fmt.Sprintf("provider API %d: %s", e.Status, body)
}

func (c *restClient) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %s", ErrNotFound, (&APIError{resp.StatusCode, string(raw)}).Error())
	}
	if resp.StatusCode >= 300 {
		return &APIError{Status: resp.StatusCode, Body: string(raw)}
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("decode %s %s: %w", method, path, err)
		}
	}
	return nil
}

// poll calls f every interval until it reports done, fails, or ctx ends.
func poll(ctx context.Context, interval, timeout time.Duration, f func() (bool, error)) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		done, err := f()
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting: %w", ctx.Err())
		case <-t.C:
		}
	}
}

// pollInterval is shortened by tests.
var pollInterval = 3 * time.Second
