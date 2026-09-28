package browser

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"hubctl/internal/config"
)

func launchConfig(t *testing.T) config.Browser {
	t.Helper()
	cfg := config.DefaultBrowser()
	// Use a known executable for lookup only; these tests never exec a browser.
	var err error
	cfg.Executable, err = os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg.UserDataDir = filepath.Join(t.TempDir(), "profile with spaces")
	return cfg
}

func TestLaunchArguments(t *testing.T) {
	cfg := launchConfig(t)
	cfg.Endpoint = "http://127.0.0.1:9333"
	p, err := planLaunch(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, arg := range []string{"--kiosk", "--password-store=basic", "--user-data-dir=" + cfg.UserDataDir, "--remote-debugging-port=9333", "--remote-debugging-address=127.0.0.1"} {
		if !slices.Contains(p.args, arg) {
			t.Fatalf("missing %s in %v", arg, p.args)
		}
	}
	if got := p.args[len(p.args)-2:]; got[0] != cfg.Tabs[0].URL || got[1] != cfg.Tabs[1].URL {
		t.Fatal(got)
	}
	t.Setenv("HOME", t.TempDir())
	cfg.UserDataDir = ""
	p, err = planLaunch(cfg)
	if err != nil || p.profile != filepath.Join(os.Getenv("HOME"), ".config", "hubctl-chromium") {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestLaunchValidation(t *testing.T) {
	for _, endpoint := range []string{"http://[::1]:9222", "http://0.0.0.0:9222", "http://127.0.0.1:0", "http://127.0.0.1:65536", "https://127.0.0.1:9222"} {
		cfg := launchConfig(t)
		cfg.Endpoint = endpoint
		if _, err := planLaunch(cfg); err == nil {
			t.Fatal(endpoint)
		}
	}
	cfg := launchConfig(t)
	cfg.Executable = "/missing/hubctl-test-chromium"
	if _, err := planLaunch(cfg); err == nil {
		t.Fatal("missing executable accepted")
	}
}

func TestLaunchPreflight(t *testing.T) {
	cfg := launchConfig(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	cfg.Endpoint = "http://" + l.Addr().String()
	p, err := planLaunch(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.preflight(context.Background()); err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatal(err)
	}
	l.Close()
	if err := p.preflight(context.Background()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p.profile)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("%v %v", info, err)
	}
	// Preserve profile data across launches.
	file := filepath.Join(p.profile, "Preferences")
	if err := os.WriteFile(file, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := p.preflight(context.Background()); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(file); string(data) != "keep" {
		t.Fatal("profile changed")
	}
	lock := filepath.Join(p.profile, "SingletonLock")
	if err := os.Symlink("panel-12345", lock); err != nil {
		t.Fatal(err)
	}
	if err := p.preflight(context.Background()); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(lock); err != nil || target != "panel-12345" {
		t.Fatalf("Chromium's lock was changed: target=%q error=%v", target, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.preflight(ctx); err != context.Canceled {
		t.Fatal(err)
	}
}
