// Package config loads and validates hubctl configuration.
package config

import (
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	Socket   Socket   `toml:"socket"`
	Display  Display  `toml:"display"`
	Browser  Browser  `toml:"browser"`
	Input    Input    `toml:"input"`
	Policy   Policy   `toml:"policy"`
	MQTT     MQTT     `toml:"mqtt"`
	Settings Settings `toml:"settings"`
}

type Settings struct {
	Path string `toml:"path"`
}

type MQTT struct {
	Enabled         bool   `toml:"enabled"`
	Broker          string `toml:"broker"`
	DeviceID        string `toml:"device_id"`
	DeviceName      string `toml:"device_name"`
	TopicPrefix     string `toml:"topic_prefix"`
	DiscoveryPrefix string `toml:"discovery_prefix"`
	BirthTopic      string `toml:"birth_topic"`
}

func DefaultMQTT() MQTT {
	return MQTT{Broker: "tcp://127.0.0.1:1883", DeviceID: "kitchen_hub", DeviceName: "Kitchen Hub", TopicPrefix: "hub/kitchen", DiscoveryPrefix: "homeassistant", BirthTopic: "homeassistant/status"}
}
func (m MQTT) Validate() error {
	u, err := url.Parse(m.Broker)
	if err != nil || (u.Scheme != "tcp" && u.Scheme != "ssl") || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("mqtt.broker must be tcp://host:port or ssl://host:port, without embedded credentials")
	}
	if m.DeviceID == "" {
		return fmt.Errorf("mqtt.device_id must not be empty")
	}
	for _, r := range m.DeviceID {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return fmt.Errorf("mqtt.device_id must use letters, digits, underscores, or hyphens")
		}
	}
	if strings.TrimSpace(m.DeviceName) == "" {
		return fmt.Errorf("mqtt.device_name must not be empty")
	}
	for _, topic := range []string{m.TopicPrefix, m.DiscoveryPrefix, m.BirthTopic} {
		if topic == "" || strings.ContainsAny(topic, "+#\x00\r\n") || strings.Trim(topic, "/ ") != topic {
			return fmt.Errorf("MQTT topics/prefixes must be nonempty without wildcards or surrounding slashes/spaces")
		}
	}
	if m.BirthTopic == m.TopicPrefix+"/mode/set" || strings.HasPrefix(m.BirthTopic, m.TopicPrefix+"/settings/") {
		return fmt.Errorf("mqtt.birth_topic must differ from the command topic")
	}
	return nil
}

type Policy struct {
	Enabled    bool   `toml:"enabled"`
	DayIdle    string `toml:"day_idle"`
	NightIdle  string `toml:"night_idle"`
	NightStart string `toml:"night_start"`
	NightEnd   string `toml:"night_end"`
	Timezone   string `toml:"timezone"`
}

func DefaultPolicy() Policy {
	return Policy{DayIdle: "5m", NightIdle: "2m", NightStart: "22:00", NightEnd: "07:00", Timezone: "Local"}
}

func (p Policy) Validate() error {
	for name, value := range map[string]string{"day_idle": p.DayIdle, "night_idle": p.NightIdle} {
		d, err := time.ParseDuration(value)
		if err != nil || d <= 0 {
			return fmt.Errorf("policy.%s must be a positive duration", name)
		}
	}
	for name, value := range map[string]string{"night_start": p.NightStart, "night_end": p.NightEnd} {
		parsed, err := time.Parse("15:04", value)
		if err != nil || parsed.Format("15:04") != value {
			return fmt.Errorf("policy.%s must use HH:MM", name)
		}
	}
	if p.NightStart == p.NightEnd {
		return fmt.Errorf("policy night_start and night_end must differ")
	}
	if _, err := time.LoadLocation(p.Timezone); err != nil || p.Timezone == "" {
		return fmt.Errorf("policy.timezone must be Local or a valid IANA timezone")
	}
	return nil
}

type Input struct {
	Device    string `toml:"device"`
	WakeDelay string `toml:"wake_delay"`
}

func (i Input) WakeDuration() (time.Duration, error) {
	if i.WakeDelay == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(i.WakeDelay)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("input.wake_delay must be a nonnegative duration such as 4s")
	}
	return d, nil
}

type Socket struct {
	Path string `toml:"path"`
}
type Display struct {
	Output string `toml:"output"`
}
type Browser struct {
	Executable     string `toml:"executable"`
	UserDataDir    string `toml:"user_data_dir"`
	Endpoint       string `toml:"endpoint"`
	ActiveTab      string `toml:"active_tab"`
	ScreensaverTab string `toml:"screensaver_tab"`
	Tabs           []Tab  `toml:"tabs"`
}

type Tab struct {
	Name string `toml:"name"`
	URL  string `toml:"url"`
}

func DefaultBrowser() Browser {
	return Browser{Endpoint: "http://127.0.0.1:9222", ActiveTab: "home", ScreensaverTab: "slideshow", Tabs: []Tab{{Name: "home", URL: "http://homeassistant.local"}, {Name: "slideshow", URL: "https://www.google.com"}}}
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
	c := Config{Browser: DefaultBrowser(), Policy: DefaultPolicy(), MQTT: DefaultMQTT()}
	if runtimeDir != "" {
		c.Socket.Path = filepath.Join(runtimeDir, "hubctl.sock")
	}
	if err := toml.NewDecoder(r).DisallowUnknownFields().Decode(&c); err != nil {
		return Config{}, fmt.Errorf("decode TOML: %w", err)
	}
	if err := c.Policy.Validate(); err != nil {
		return Config{}, err
	}
	if err := c.MQTT.Validate(); err != nil {
		return Config{}, err
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c Config) Validate() error {
	if strings.ContainsRune(c.Browser.Executable, '\x00') || (c.Browser.Executable != "" && strings.TrimSpace(c.Browser.Executable) == "") {
		return fmt.Errorf("browser.executable must name an executable, not a shell command")
	}
	if p := c.Browser.UserDataDir; p != "" && (!filepath.IsAbs(p) || strings.ContainsRune(p, '\x00') || filepath.Clean(p) == "/") {
		return fmt.Errorf("browser.user_data_dir must be an absolute profile directory")
	}
	if c.Settings.Path != "" && (!filepath.IsAbs(c.Settings.Path) || strings.ContainsRune(c.Settings.Path, '\x00') || filepath.Clean(c.Settings.Path) == "/") {
		return fmt.Errorf("settings.path must be an absolute file path")
	}
	if c.MQTT.Enabled {
		if err := c.MQTT.Validate(); err != nil {
			return err
		}
	}
	if c.Policy.Enabled {
		if err := c.Policy.Validate(); err != nil {
			return err
		}
		if c.Input.Device == "" {
			return fmt.Errorf("policy requires input.device for touch activity and wake")
		}
	}
	if _, err := c.Input.WakeDuration(); err != nil {
		return err
	}
	if c.Input.Device != "" && (!filepath.IsAbs(c.Input.Device) || strings.ContainsRune(c.Input.Device, '\x00')) {
		return fmt.Errorf("input.device must be an absolute device path")
	}
	if !filepath.IsAbs(c.Socket.Path) || strings.ContainsRune(c.Socket.Path, '\x00') {
		return fmt.Errorf("socket.path must be an absolute path (set it explicitly when XDG_RUNTIME_DIR is unavailable)")
	}
	if filepath.Clean(c.Socket.Path) == string(filepath.Separator) {
		return fmt.Errorf("socket.path must name a socket, not the filesystem root")
	}
	if strings.TrimSpace(c.Display.Output) == "" || strings.ContainsRune(c.Display.Output, '\x00') {
		return fmt.Errorf("display.output must name an output; discover it with wlr-randr on the Pi")
	}
	u, err := url.Parse(c.Browser.Endpoint)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("browser.endpoint must be an HTTP loopback URL without credentials, path, query, or fragment")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("browser.endpoint must use a loopback IP address")
	}
	names := map[string]bool{}
	urls := map[string]bool{}
	for _, tab := range c.Browser.Tabs {
		if strings.TrimSpace(tab.Name) == "" || names[tab.Name] {
			return fmt.Errorf("browser.tabs names must be nonempty and unique")
		}
		names[tab.Name] = true
		u, err := url.Parse(tab.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("browser.tabs URL for %q must be HTTP(S) without credentials, query, or fragment", tab.Name)
		}
		key := strings.TrimRight(u.String(), "/")
		if urls[key] {
			return fmt.Errorf("browser.tabs URLs must be unique")
		}
		urls[key] = true
	}
	if c.Browser.ActiveTab == c.Browser.ScreensaverTab {
		return fmt.Errorf("browser active_tab and screensaver_tab must be different")
	}
	if !names[c.Browser.ActiveTab] || !names[c.Browser.ScreensaverTab] {
		return fmt.Errorf("browser mode mappings must reference configured tab names")
	}
	return nil
}
