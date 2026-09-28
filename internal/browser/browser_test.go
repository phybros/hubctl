package browser

import (
	"context"
	"encoding/json"
	"hubctl/internal/config"
	"hubctl/internal/controller"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestActivationAndRediscovery(t *testing.T) {
	id := "first"
	var activated string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/json/list" {
			json.NewEncoder(w).Encode([]target{{ID: "worker", Type: "service_worker", URL: "http://homeassistant.local"}, {ID: id, Type: "page", URL: "http://homeassistant.local/lovelace/0?x=1"}, {ID: "photos", Type: "page", URL: "https://www.google.com/"}})
			return
		}
		activated = strings.TrimPrefix(r.URL.Path, "/json/activate/")
		w.Write([]byte("Target activated"))
	}))
	defer server.Close()
	cfg := config.DefaultBrowser()
	cfg.Endpoint = server.URL
	b := New(cfg)
	for _, current := range []string{"first", "after-restart"} {
		id = current
		if err := b.Select(context.Background(), controller.Active); err != nil {
			t.Fatal(err)
		}
		if activated != current {
			t.Fatalf("activated %q", activated)
		}
	}
	if err := b.Select(context.Background(), controller.Screensaver); err != nil {
		t.Fatal(err)
	}
	if activated != "photos" {
		t.Fatal(activated)
	}
	if err := b.Select(context.Background(), controller.DisplayOff); err == nil {
		t.Fatal("invalid mode accepted")
	}
}

func TestFailures(t *testing.T) {
	for _, tt := range []struct {
		name, body, want string
		status           int
	}{
		{"missing", "[]", "not open", 200},
		{"ambiguous", `[{"id":"a","type":"page","url":"http://homeassistant.local"},{"id":"b","type":"page","url":"http://homeassistant.local/other"}]`, "ambiguous", 200},
		{"invalid JSON", "{", "decode", 200},
		{"HTTP error", "", "HTTP 500", 500},
		{"redirect", "", "HTTP 302", 302},
		{"empty ID", `[{"type":"page","url":"http://homeassistant.local"}]`, "empty target ID", 200},
		{"large", strings.Repeat("x", 1024*1024+1), "exceeds", 200},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/json/list" {
					t.Error("unexpected activation")
				}
				w.WriteHeader(tt.status)
				w.Write([]byte(tt.body))
			}))
			defer server.Close()
			cfg := config.DefaultBrowser()
			cfg.Endpoint = server.URL
			err := New(cfg).Select(context.Background(), controller.Active)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error: %v", err)
			}
		})
	}
}

func TestActivationFailureAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/json/list" {
			w.Write([]byte(`[{"id":"gone","type":"page","url":"http://homeassistant.local"}]`))
			return
		}
		w.WriteHeader(404)
	}))
	defer server.Close()
	cfg := config.DefaultBrowser()
	cfg.Endpoint = server.URL
	if err := New(cfg).Select(context.Background(), controller.Active); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("error: %v", err)
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer slow.Close()
	cfg.Endpoint = slow.URL
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := New(cfg).Select(ctx, controller.Active); err == nil {
		t.Fatal("expected timeout")
	}
}

func TestURLMatching(t *testing.T) {
	for _, tt := range []struct {
		url   string
		match bool
	}{
		{"http://ha.local/panel", true}, {"http://ha.local/panel/room?q=1#x", true}, {"http://ha.local/panels", false}, {"http://ha.local.evil/panel", false}, {"https://ha.local/panel", false},
	} {
		if got := matchesURL("http://ha.local/panel", tt.url); got != tt.match {
			t.Errorf("%s: %t", tt.url, got)
		}
	}
}
