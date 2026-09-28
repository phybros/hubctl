// Package browser activates existing Chromium tabs through its DevTools HTTP API.
package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"hubctl/internal/config"
	"hubctl/internal/controller"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type DevTools struct {
	config config.Browser
	client *http.Client
}

func New(cfg config.Browser) *DevTools {
	return &DevTools{config: cfg, client: &http.Client{Timeout: time.Second,
		// Do not proxy or follow redirects from the local browser-control endpoint.
		Transport:     &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

type target struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	URL  string `json:"url"`
}

func (b *DevTools) Select(ctx context.Context, mode controller.Mode) error {
	var name string
	switch mode {
	case controller.Active:
		name = b.config.ActiveTab
	case controller.Screensaver:
		name = b.config.ScreensaverTab
	default:
		return fmt.Errorf("cannot select browser tab for mode %q", mode)
	}
	var wanted string
	for _, tab := range b.config.Tabs {
		if tab.Name == name {
			wanted = tab.URL
			break
		}
	}
	if wanted == "" {
		return fmt.Errorf("tab %q is not configured", name)
	}
	data, err := b.get(ctx, "/json/list")
	if err != nil {
		return err
	}
	var targets []target
	if err := json.Unmarshal(data, &targets); err != nil {
		return fmt.Errorf("decode DevTools tab list: %w", err)
	}
	var matches []target
	for _, t := range targets {
		if t.Type == "page" && matchesURL(wanted, t.URL) {
			matches = append(matches, t)
		}
	}
	if len(matches) == 0 {
		return fmt.Errorf("tab %q is not open; open its configured URL in Chromium", name)
	}
	if len(matches) != 1 {
		return fmt.Errorf("tab %q is ambiguous: %d matching pages are open", name, len(matches))
	}
	if matches[0].ID == "" {
		return fmt.Errorf("DevTools returned an empty target ID for tab %q", name)
	}
	_, err = b.get(ctx, "/json/activate/"+url.PathEscape(matches[0].ID))
	return err
}

// Match origin and path subtree, ignoring query/fragment, to allow in-app navigation.
func matchesURL(wanted, actual string) bool {
	w, err := url.Parse(wanted)
	if err != nil {
		return false
	}
	a, err := url.Parse(actual)
	if err != nil {
		return false
	}
	if !strings.EqualFold(w.Scheme, a.Scheme) || !strings.EqualFold(w.Host, a.Host) {
		return false
	}
	base := strings.TrimRight(w.EscapedPath(), "/")
	return a.EscapedPath() == base || strings.HasPrefix(a.EscapedPath(), base+"/")
}
func (b *DevTools) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(b.config.Endpoint, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("DevTools request failed (is Chromium running with remote debugging enabled?): %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DevTools %s returned HTTP %d", path, resp.StatusCode)
	}
	const limit = 1024 * 1024
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read DevTools response: %w", err)
	}
	if len(data) > limit {
		return nil, fmt.Errorf("DevTools response exceeds 1 MiB")
	}
	return data, nil
}
