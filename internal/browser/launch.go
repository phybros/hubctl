package browser

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"hubctl/internal/config"
)

type launchPlan struct {
	executable string
	profile    string
	address    string
	args       []string
}

func planLaunch(cfg config.Browser) (launchPlan, error) {
	var p launchPlan
	name := cfg.Executable
	if name == "" {
		name = "chromium"
	}
	var err error
	p.executable, err = exec.LookPath(name)
	if err != nil {
		return p, fmt.Errorf("find Chromium (%s): %w; set browser.executable if needed", name, err)
	}
	p.profile = cfg.UserDataDir
	if p.profile == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return p, err
		}
		// Preserve the profile used by the original manual launch instructions.
		p.profile = filepath.Join(home, ".config", "hubctl-chromium")
	}
	if !filepath.IsAbs(p.profile) || filepath.Clean(p.profile) == "/" {
		return p, fmt.Errorf("browser profile must be an absolute non-root directory")
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return p, err
	}
	// Chromium's DevTools server chooses its loopback listener itself. Restrict
	// launch to IPv4 localhost so the daemon's endpoint matches that listener.
	if u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return p, fmt.Errorf("browser launch requires endpoint http://127.0.0.1:PORT")
	}
	port := u.Port()
	if port == "" {
		port = "80"
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return p, fmt.Errorf("browser endpoint port must be between 1 and 65535")
	}
	p.address = net.JoinHostPort("127.0.0.1", port)
	p.args = []string{"--kiosk", "--noerrdialogs", "--no-first-run", "--password-store=basic", "--user-data-dir=" + p.profile, "--remote-debugging-address=127.0.0.1", "--remote-debugging-port=" + port}
	if len(cfg.Tabs) == 0 {
		return p, fmt.Errorf("no browser tabs configured")
	}
	for _, tab := range cfg.Tabs {
		p.args = append(p.args, tab.URL)
	}
	return p, nil
}

func (p launchPlan) preflight(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Chromium owns profile locking and stale-lock recovery. Do not reject or
	// delete its Singleton files here: their presence does not prove it is running.
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", p.address)
	if err == nil {
		conn.Close()
		return fmt.Errorf("browser endpoint %s is already in use; Chromium may already be running", p.address)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("check browser endpoint: %w", err)
	}
	if err := os.MkdirAll(p.profile, 0700); err != nil {
		return fmt.Errorf("create browser profile: %w", err)
	}
	return ctx.Err()
}
