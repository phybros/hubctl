package settings

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"hubctl/internal/config"
)

func TestPersistenceDefaultsAndReset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "settings.json")
	cfg := config.DefaultPolicy()
	s, err := Open(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Change(ctx, "day_idle", "10m", false); err != nil {
		t.Fatal(err)
	}
	if err := s.Change(ctx, "night_start", "23:00", false); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	cfg.DayIdle = "7m"
	s, err = Open(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	v := s.Snapshot().Values["day_idle"]
	if v.Value != "10m" || v.Default != "7m" || v.Source != "override" {
		t.Fatal(v)
	}
	if err := s.Change(ctx, "day_idle", "", true); err != nil {
		t.Fatal(err)
	}
	if s.Policy().DayIdle != "7m" || s.Snapshot().Values["day_idle"].Source != "config" {
		t.Fatal(s.Snapshot())
	}
	if err := s.Change(ctx, "all", "", true); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path, cfg)
	if err != nil || s.Policy() != cfg {
		t.Fatalf("%v %v", s, err)
	}
}

func TestRejectedChangesAreAtomic(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "settings.json"), config.DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range [][2]string{{"day_idle", "0s"}, {"night_idle", "-1m"}, {"night_start", "7:00"}, {"night_start", "07:00"}, {"night_end", "24:00"}, {"timezone", "UTC"}} {
		if err := s.Change(context.Background(), tc[0], tc[1], false); err == nil {
			t.Fatal(tc)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Change(ctx, "day_idle", "10m", false); err == nil {
		t.Fatal("cancellation ignored")
	}
	// A write failure must not reach live policy or change published settings.
	if err := os.Mkdir(s.path, 0700); err != nil {
		t.Fatal(err)
	}
	applied := false
	s.Bind(func(_ context.Context, _ config.Policy, commit func() error) error {
		if err := commit(); err != nil {
			return err
		}
		applied = true
		return nil
	})
	if err := s.Change(context.Background(), "day_idle", "10m", false); err == nil {
		t.Fatal("write unexpectedly succeeded")
	}
	if applied || s.Policy().DayIdle != "5m" {
		t.Fatal("failed write changed settings")
	}
}

func TestCorruptStateFailsClosed(t *testing.T) {
	for _, data := range []string{`{`, `{"version":2}`, `{"version":1,"overrides":{"bogus":"1"}}`, `{"version":1,"overrides":{"night_start":"07:00"}}`, `{"version":1} {}`, `{"version":1,"extra":true}`} {
		path := filepath.Join(t.TempDir(), "settings.json")
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(path, config.DefaultPolicy()); err == nil {
			t.Fatal(data)
		}
	}
}

func TestConcurrentUpdates(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "settings.json"), config.DefaultPolicy())
	var wg sync.WaitGroup
	for _, name := range []string{"day_idle", "night_idle"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			if err := s.Change(context.Background(), name, "10m", false); err != nil {
				t.Error(err)
			}
			_ = s.Snapshot()
		}(name)
	}
	wg.Wait()
	if s.Policy().DayIdle != "10m" || s.Policy().NightIdle != "10m" {
		t.Fatal(s.Policy())
	}
}

func TestDefaultPath(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	path, err := DefaultPath()
	if err != nil || path != filepath.Join(os.Getenv("XDG_STATE_HOME"), "hubctl", "settings.json") {
		t.Fatalf("%s %v", path, err)
	}
	t.Setenv("XDG_STATE_HOME", "relative")
	if _, err := DefaultPath(); err == nil {
		t.Fatal("relative state path accepted")
	}
}
