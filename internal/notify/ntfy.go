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

// NtfyConfig is the persisted JSON config for the ntfy backend (§7).
type NtfyConfig struct {
	URL   string `json:"url"`   // e.g. https://ntfy.sh or a self-hosted instance
	Topic string `json:"topic"` // publish topic
	Token string `json:"token"` // optional auth for private topics
}

type ntfySender struct {
	cfg    NtfyConfig
	client *http.Client
}

func NtfyFactory(raw json.RawMessage) (Sender, error) {
	var c NtfyConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	c.URL = strings.TrimRight(c.URL, "/")
	if c.URL == "" || c.Topic == "" {
		return nil, fmt.Errorf("ntfy: url and topic are required")
	}
	return &ntfySender{cfg: c, client: &http.Client{Timeout: 10 * time.Second}}, nil
}

func (s *ntfySender) Name() string { return "ntfy" }

func (s *ntfySender) Send(ctx context.Context, p Payload) error {
	body := map[string]any{
		"title":   p.Title,
		"message": p.Message,
		"tags":    []string{"money"},
	}
	if p.Link != "" {
		body["click"] = p.Link
		body["actions"] = []map[string]any{{
			"action": "view", "label": "Open", "url": p.Link,
		}}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.cfg.URL+"/"+s.cfg.Topic, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+s.cfg.Token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("ntfy: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy: %s returned %s", s.cfg.URL, resp.Status)
	}
	return nil
}
