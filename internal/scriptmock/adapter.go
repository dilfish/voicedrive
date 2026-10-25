package scriptmock

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// HTTPAdapter calls a local test-only device ingress; the device implements routing.
type HTTPAdapter struct {
	endpoint string
	client   *http.Client
}

// NewHTTPAdapter limits automatic injection to explicitly configured loopback HTTP.
func NewHTTPAdapter(endpoint string) (*HTTPAdapter, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("adapter URL: %w", err)
	}
	if u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("adapter must be a loopback HTTP URL without credentials/query/fragment")
	}
	return &HTTPAdapter{endpoint: endpoint, client: &http.Client{Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (a *HTTPAdapter) post(ctx context.Context, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode adapter request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("adapter request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("adapter transport: %w", err)
	}
	if err := resp.Body.Close(); err != nil {
		return fmt.Errorf("close adapter response: %w", err)
	}
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("BLOCKED: device adapter returned status %d, expected 204", resp.StatusCode)
	}
	return nil
}

// Input delivers text to the device's real input entry point.
func (a *HTTPAdapter) Input(ctx context.Context, text string) error {
	return a.post(ctx, struct {
		Op   string `json:"op"`
		Text string `json:"text"`
	}{"input", text})
}

// Advance requests deterministic clock advancement from the device test loop.
func (a *HTTPAdapter) Advance(ctx context.Context, d time.Duration) error {
	return a.post(ctx, struct {
		Op string `json:"op"`
		MS int64  `json:"advance_ms"`
	}{"advance", d.Milliseconds()})
}
