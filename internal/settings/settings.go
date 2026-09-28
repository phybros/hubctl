// Package settings owns validated, persistent overrides of TOML policy defaults.
package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"sync"

	"hubctl/internal/config"
)

var Names = []string{"day_idle", "night_idle", "night_start", "night_end"}

type Value struct {
	Value   string `json:"value"`
	Default string `json:"default"`
	Source  string `json:"source"`
}
type Snapshot struct {
	Path          string           `json:"path"`
	PolicyEnabled bool             `json:"policy_enabled"`
	Values        map[string]Value `json:"values"`
}
type document struct {
	Version   int               `json:"version"`
	Overrides map[string]string `json:"overrides"`
}

// Apply must call commit before changing the live policy, under its action gate.
type Apply func(context.Context, config.Policy, func() error) error
type Store struct {
	mu        sync.Mutex
	path      string
	defaults  config.Policy
	overrides map[string]string
	apply     Apply
}

func DefaultPath() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	if !filepath.IsAbs(base) {
		return "", fmt.Errorf("XDG_STATE_HOME must be absolute")
	}
	return filepath.Join(base, "hubctl", "settings.json"), nil
}

func Open(path string, defaults config.Policy) (*Store, error) {
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return nil, err
		}
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("settings path must be absolute")
	}
	s := &Store{path: path, defaults: defaults, overrides: map[string]string{}}
	f, err := os.Open(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Size() > 16384 {
			return nil, fmt.Errorf("settings file must be a regular file no larger than 16 KiB")
		}
		d := json.NewDecoder(io.LimitReader(f, 16385))
		d.DisallowUnknownFields()
		var doc document
		if err := d.Decode(&doc); err != nil {
			return nil, fmt.Errorf("read settings %s: %w", path, err)
		}
		var extra any
		if err := d.Decode(&extra); err != io.EOF {
			return nil, fmt.Errorf("settings file must contain one JSON document")
		}
		if doc.Version != 1 {
			return nil, fmt.Errorf("unsupported settings version %d", doc.Version)
		}
		if doc.Overrides != nil {
			s.overrides = doc.Overrides
		}
	}
	if _, err := s.effective(s.overrides); err != nil {
		return nil, fmt.Errorf("settings %s: %w", path, err)
	}
	return s, nil
}

// Bind is called once before serving requests.
func (s *Store) Bind(apply Apply) { s.apply = apply }
func values(p config.Policy) map[string]string {
	return map[string]string{"day_idle": p.DayIdle, "night_idle": p.NightIdle, "night_start": p.NightStart, "night_end": p.NightEnd}
}
func (s *Store) effective(overrides map[string]string) (config.Policy, error) {
	p := s.defaults
	for name, value := range overrides {
		switch name {
		case "day_idle":
			p.DayIdle = value
		case "night_idle":
			p.NightIdle = value
		case "night_start":
			p.NightStart = value
		case "night_end":
			p.NightEnd = value
		default:
			return p, fmt.Errorf("unknown setting %q", name)
		}
	}
	return p, p.Validate()
}
func (s *Store) Policy() config.Policy {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, _ := s.effective(s.overrides)
	return p
}
func (s *Store) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, _ := s.effective(s.overrides)
	out := Snapshot{Path: s.path, PolicyEnabled: p.Enabled, Values: map[string]Value{}}
	defaults := values(s.defaults)
	for name, value := range values(p) {
		source := "config"
		if _, ok := s.overrides[name]; ok {
			source = "override"
		}
		out.Values[name] = Value{Value: value, Default: defaults[name], Source: source}
	}
	return out
}

// Change sets one override, or removes it when reset is true. Resetting "all"
// removes all overrides atomically, useful for interdependent schedule values.
func (s *Store) Change(ctx context.Context, name, value string, reset bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := values(s.defaults)[name]; !ok && !(reset && name == "all") {
		return fmt.Errorf("unknown setting %q", name)
	}
	next := map[string]string{}
	for k, v := range s.overrides {
		next[k] = v
	}
	if reset {
		if name == "all" {
			clear(next)
		} else {
			delete(next, name)
		}
	} else {
		next[name] = value
	}
	p, err := s.effective(next)
	if err != nil {
		return err
	}
	if maps.Equal(next, s.overrides) {
		return ctx.Err()
	}
	commit := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.save(next); err != nil {
			return fmt.Errorf("persist settings: %w", err)
		}
		s.overrides = next
		return nil
	}
	if s.apply != nil {
		return s.apply(ctx, p, commit)
	}
	return commit()
}

func (s *Store) save(next map[string]string) error {
	data, err := json.MarshalIndent(document{Version: 1, Overrides: next}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".settings-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), s.path)
}
