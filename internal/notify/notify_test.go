package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestNtfySender(t *testing.T) {
	var mu sync.Mutex
	var gotURL, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		gotURL = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	raw, _ := json.Marshal(NtfyConfig{URL: srv.URL, Topic: "mytopic", Token: "tok"})
	s, err := NtfyFactory(raw)
	if err != nil {
		t.Fatal(err)
	}
	p := Payload{Event: EventNeedsCategory, Title: "T", Message: "M", Link: "http://x/inbox"}
	if err := s.Send(context.Background(), p); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotURL != "/" {
		t.Errorf("posted to %q, want the server root (topic goes in the JSON body)", gotURL)
	}
	if !strings.Contains(gotBody, `"topic":"mytopic"`) {
		t.Errorf("body missing topic field: %s", gotBody)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("auth = %q", gotAuth)
	}
	if !strings.Contains(gotBody, `"title":"T"`) || !strings.Contains(gotBody, `"click":"http://x/inbox"`) {
		t.Errorf("body wrong: %s", gotBody)
	}
}

func TestWebhookSender(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	raw, _ := json.Marshal(WebhookConfig{URL: srv.URL})
	s, err := WebhookFactory(raw)
	if err != nil {
		t.Fatal(err)
	}
	p := Payload{Event: EventSyncFailed, Title: "Boom", Message: "sync failed", AmountMinor: -501, Currency: "EUR", Merchant: "x", Account: "sandbox", Timestamp: "t", Link: "http://x"}
	if err := s.Send(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if got["event"] != "sync_failed" || got["amount_minor"] != float64(-501) || got["link"] != "http://x" {
		t.Errorf("payload wrong: %+v", got)
	}
}

// Recording sender + factory for routing tests.
type recSender struct {
	name string
	rec  *[]string
}

func (s *recSender) Name() string { return s.name }
func (s *recSender) Send(_ context.Context, p Payload) error {
	*s.rec = append(*s.rec, string(p.Event))
	return nil
}

func TestDispatcherRoutesByEvent(t *testing.T) {
	var got []string
	cfg := func(typ string, events map[Event]bool) Config {
		raw, _ := json.Marshal(map[string]string{"url": "http://x"})
		return Config{Type: typ, Config: raw, Events: events, Enabled: true}
	}
	load := func(context.Context) ([]Config, error) {
		return []Config{
			cfg("ntfy", map[Event]bool{EventNeedsCategory: true}), // only one event
			cfg("ntfy", nil), // empty = all events
			cfg("webhook", map[Event]bool{EventSyncFailed: true}),
			cfg("ntfy", nil), // disabled below → skipped
		}, nil
	}
	d := NewDispatcher(load)
	// The disabled one: flip after building.
	cfgs, _ := load(context.Background())
	cfgs[3].Enabled = false
	d.load = func(ctx context.Context) ([]Config, error) { return cfgs, nil }
	d.factories["ntfy"] = func(json.RawMessage) (Sender, error) { return &recSender{"ntfy", &got}, nil }
	d.factories["webhook"] = func(json.RawMessage) (Sender, error) { return &recSender{"webhook", &got}, nil }

	if err := d.Notify(context.Background(), Payload{Event: EventNeedsCategory}); err != nil {
		t.Fatal(err)
	}
	// Subscribed: ntfy(needs_category) + ntfy(all) = 2 sends. webhook only
	// does sync_failed; disabled skipped.
	if len(got) != 2 {
		t.Errorf("sent %d notifications for needs_category, want 2: %v", len(got), got)
	}
	got = nil
	if err := d.Notify(context.Background(), Payload{Event: EventSyncFailed}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 { // ntfy(all) + webhook(sync_failed)
		t.Errorf("sent %d for sync_failed, want 2: %v", len(got), got)
	}
}

func TestSubscribedEmptyMeansAll(t *testing.T) {
	if !(Config{}).subscribed(EventNeedsCategory) {
		t.Error("empty events map should subscribe to everything")
	}
	c := Config{Events: map[Event]bool{EventSyncFailed: true}}
	if c.subscribed(EventNeedsCategory) {
		t.Error("unlisted event should not fire")
	}
	if !c.subscribed(EventSyncFailed) {
		t.Error("listed event should fire")
	}
}

func TestNopNotifier(t *testing.T) {
	if err := (Nop{}).Notify(context.Background(), Payload{}); err != nil {
		t.Errorf("Nop should never error: %v", err)
	}
}
