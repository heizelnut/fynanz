// Package notify is the pluggable notification layer (AGENTS.md §7):
// a Notifier interface, a Dispatcher that routes payloads to every enabled
// channel subscribed to a given event, and transports (ntfy, webhook).
package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"fynanz/internal/metrics"
)

// Event identifies a triggerable notification (§7, each toggleable).
type Event string

const (
	EventNeedsCategory Event = "needs_category" // new tx awaiting a category
	EventReauthNeeded  Event = "reauth_needed"  // bank session expired/revoked
	EventSyncFailed    Event = "sync_failed"    // a sync run errored
)

// Payload is the notification contract (§7): enough context to act without
// opening the dashboard, plus a deep link straight to the action.
type Payload struct {
	Event       Event
	Title       string
	Message     string
	AmountMinor int64
	Currency    string
	Merchant    string
	Account     string
	Timestamp   string
	Link        string
}

// Notifier is what the sync layer sends through.
type Notifier interface {
	Notify(ctx context.Context, p Payload) error
}

// Config is one channel's persisted form (mirrors notification_channels).
type Config struct {
	ID      int64 // store row id — history ties attempts to their channel
	Type    string
	Config  json.RawMessage
	Events  map[Event]bool // empty map means "all events"
	Enabled bool
}

// Attempt describes one channel × payload send for history purposes.
type Attempt struct {
	ChannelID   int64
	ChannelType string
	Event       Event
	OK          bool
	Error       string
	Title       string
	Message     string
	Link        string
	Time        time.Time
}

// Observer receives every send attempt (the app records these as history;
// observers must not throw).
type Observer func(Attempt)

// Loader supplies active channel configs (the app builds it from the store).
type Loader func(ctx context.Context) ([]Config, error)

// Sender performs the transport for one channel.
type Sender interface {
	Name() string
	Send(ctx context.Context, p Payload) error
}

// Factory builds a Sender from a channel's JSON config.
type Factory func(raw json.RawMessage) (Sender, error)

// Dispatcher routes payloads to every enabled channel subscribed to the
// event; all channels are attempted, the first error is reported, and each
// send is counted for Prometheus.
type Dispatcher struct {
	load      Loader
	factories map[string]Factory
	rec       metrics.Recorder
	observe   Observer
}

func NewDispatcher(load Loader) *Dispatcher {
	return &Dispatcher{
		load: load,
		rec:  metrics.Recorder{},
		factories: map[string]Factory{
			"ntfy":    NtfyFactory,
			"webhook": WebhookFactory,
		},
	}
}

// SetObserver registers the history recorder (called once at startup).
// Attempts are recorded for every channel, success or failure.
func (d *Dispatcher) SetObserver(o Observer) { d.observe = o }

func (d *Dispatcher) record(c Config, p Payload, ok bool, errMsg string) {
	if d.observe == nil {
		return
	}
	d.observe(Attempt{
		ChannelID:   c.ID,
		ChannelType: c.Type,
		Event:       p.Event,
		OK:          ok,
		Error:       errMsg,
		Title:       p.Title,
		Message:     p.Message,
		Link:        p.Link,
		Time:        time.Now().UTC(),
	})
}

// Nop swallows notifications (unconfigured installs, tests).
type Nop struct{}

func (Nop) Notify(_ context.Context, _ Payload) error { return nil }

func (c Config) subscribed(e Event) bool {
	if len(c.Events) == 0 {
		return true // opt-out model: empty events = all
	}
	return c.Events[e]
}

func (d *Dispatcher) Notify(ctx context.Context, p Payload) error {
	if d == nil || d.load == nil {
		return nil
	}
	cfgs, err := d.load(ctx)
	if err != nil {
		return fmt.Errorf("notify: load channels: %w", err)
	}
	var firstErr error
	for _, c := range cfgs {
		if !c.Enabled || !c.subscribed(p.Event) {
			continue
		}
		fn, ok := d.factories[c.Type]
		if !ok {
			continue
		}
		ch, err := fn(c.Config)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("notify: configure %s: %w", c.Type, err)
			}
			d.rec.NotificationFailed(c.Type, string(p.Event))
			d.record(c, p, false, "configure: "+err.Error())
			continue
		}
		if err := ch.Send(ctx, p); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			d.rec.NotificationFailed(c.Type, string(p.Event))
			d.record(c, p, false, err.Error())
			continue
		}
		d.rec.NotificationSent(c.Type, string(p.Event))
		d.record(c, p, true, "")
	}
	return firstErr
}
