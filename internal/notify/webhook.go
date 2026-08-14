package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// WebhookConfig is the persisted JSON config for the webhook backend (§7):
// a plain POST of the full payload, chainable into n8n / Home Assistant /
// a Slack relay — no assumptions about the receiver.
type WebhookConfig struct {
	URL string `json:"url"`
}

type webhookSender struct {
	cfg    WebhookConfig
	client *http.Client
}

func WebhookFactory(raw json.RawMessage) (Sender, error) {
	var c WebhookConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	c.URL = strings.TrimSpace(c.URL)
	if c.URL == "" {
		return nil, fmt.Errorf("webhook: url is required")
	}
	return &webhookSender{cfg: c, client: &http.Client{Timeout: 10 * time.Second}}, nil
}

func (s *webhookSender) Name() string { return "webhook" }

func (s *webhookSender) Send(ctx context.Context, p Payload) error {
	body := map[string]any{
		"event":        p.Event,
		"title":        p.Title,
		"message":      p.Message,
		"amount_minor": p.AmountMinor,
		"currency":     p.Currency,
		"merchant":     p.Merchant,
		"account":      p.Account,
		"timestamp":    p.Timestamp,
		"link":         p.Link,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.URL, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook: %s returned %s", s.cfg.URL, resp.Status)
	}
	return nil
}
