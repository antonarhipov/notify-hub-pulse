package pulse

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Sender lets the worker pool use either the HTTP client or a test double.
type Sender interface {
	Send(context.Context, Notification) error
}

type Client struct {
	endpoint string
	http     *http.Client
}

func NewClient(baseURL string, timeout time.Duration) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return nil, fmt.Errorf("base URL must be an HTTP(S) URL without credentials, query, or fragment")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("request timeout must be positive")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/notify"
	u.RawPath = ""
	return &Client{endpoint: u.String(), http: &http.Client{
		Timeout: timeout,
		// A redirect must not turn a POST into a GET or move demo traffic elsewhere.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (c *Client) Close() { c.http.CloseIdleConnections() }

func (c *Client) Send(ctx context.Context, notification Notification) error {
	body, err := json.Marshal(notification)
	if err != nil {
		return fmt.Errorf("encode notification: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "notify-hub-pulse/0.1")
	response, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("send notification: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return fmt.Errorf("NotifyHub returned HTTP %d", response.StatusCode)
	}
	var result struct {
		Success        bool   `json:"success"`
		NotificationID string `json:"notificationId"`
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil {
		return fmt.Errorf("read NotifyHub response: %w", err)
	}
	if len(data) > 64<<10 {
		return fmt.Errorf("NotifyHub response exceeds 64 KiB")
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("invalid NotifyHub JSON response: %w", err)
	}
	if !result.Success || result.NotificationID == "" {
		return fmt.Errorf("NotifyHub did not confirm acceptance with a notificationId")
	}
	return nil
}
