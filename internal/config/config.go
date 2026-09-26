// Package config loads and validates hubctl configuration.
package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	Socket  Socket  `toml:"socket"`
	Display Display `toml:"display"`
	Browser Browser `toml:"browser"`
}

type Socket struct {
	Path string `toml:"path"`
}
type Display struct {
	Output string `toml:"output"`
}
type Browser struct {
	ActiveTab      int `toml:"active_tab"`
	ScreensaverTab int `toml:"screensaver_tab"`
}

// DefaultPath follows the user's configuration directory convention.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "hubctl", "config.toml"), nil
}

func Load(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config: %w", err)
	}
	defer f.Close()
	c, err := Decode(f, os.Getenv("XDG_RUNTIME_DIR"))
	if err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	return c, nil
}

// Decode defaults the socket under the graphical session's runtime directory.
// Without that directory (for example on macOS), an explicit path is required.
func Decode(r io.Reader, runtimeDir string) (Config, error) {
	c := Config{Browser: Browser{ActiveTab: 1, ScreensaverTab: 2}}
	if runtimeDir != "" {
		c.Socket.Path = filepath.Join(runtimeDir, "hubctl.sock")
	}
	if err := toml.NewDecoder(r).DisallowUnknownFields().Decode(&c); err != nil {
		return Config{}, fmt.Errorf("decode TOML: %w", err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c Config) Validate() error {
	if !filepath.IsAbs(c.Socket.Path) || strings.ContainsRune(c.Socket.Path, '\x00') {
		return fmt.Errorf("socket.path must be an absolute path (set it explicitly when XDG_RUNTIME_DIR is unavailable)")
	}
	if filepath.Clean(c.Socket.Path) == string(filepath.Separator) {
		return fmt.Errorf("socket.path must name a socket, not the filesystem root")
	}
	if strings.TrimSpace(c.Display.Output) == "" || strings.ContainsRune(c.Display.Output, '\x00') {
		return fmt.Errorf("display.output must name an output; discover it with wlr-randr on the Pi")
	}
	// Ctrl+9 means the last tab, rather than tab nine.
	if c.Browser.ActiveTab < 1 || c.Browser.ActiveTab > 8 || c.Browser.ScreensaverTab < 1 || c.Browser.ScreensaverTab > 8 {
		return fmt.Errorf("browser tab numbers must be between 1 and 8")
	}
	if c.Browser.ActiveTab == c.Browser.ScreensaverTab {
		return fmt.Errorf("browser tab numbers must be different")
	}
	return nil
}
