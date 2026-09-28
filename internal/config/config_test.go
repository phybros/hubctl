package config

import (
	"strings"
	"testing"
	"time"
)

func TestWakeDelayConfig(t *testing.T) {
	for _, value := range []string{"4s", "0s", "250ms", "-1s", "invalid"} {
		c, err := Decode(strings.NewReader("[display]\noutput='HDMI-A-1'\n[input]\nwake_delay='"+value+"'"), "/run/user/1000")
		if value == "-1s" || value == "invalid" {
			if err == nil {
				t.Fatalf("accepted %q", value)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		got, err := c.Input.WakeDuration()
		want, _ := time.ParseDuration(value)
		if err != nil || got != want {
			t.Fatalf("duration: %v %v", got, err)
		}
	}
}

func TestPolicyConfig(t *testing.T) {
	base := "[display]\noutput='HDMI-A-1'\n[input]\ndevice='/dev/input/by-id/touch'\n[policy]\nenabled=true\n"
	c, err := Decode(strings.NewReader(base), "/run/user/1000")
	if err != nil {
		t.Fatal(err)
	}
	if c.Policy.DayIdle != "5m" || c.Policy.NightStart != "22:00" || c.Policy.Timezone != "Local" {
		t.Fatalf("defaults: %+v", c.Policy)
	}
	for _, bad := range []string{"day_idle='0s'", "night_idle='-1s'", "night_start='25:00'", "night_end='22:00'", "timezone='Not/AZone'", "night_start='7:00'"} {
		if _, err := Decode(strings.NewReader(base+bad), "/run/user/1000"); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	if _, err := Decode(strings.NewReader("[display]\noutput='HDMI-A-1'\n[policy]\nenabled=true"), "/run/user/1000"); err == nil {
		t.Fatal("policy without touch accepted")
	}
}

func TestDecodeDefaults(t *testing.T) {
	c, err := Decode(strings.NewReader("[display]\noutput = 'HDMI-A-1'\n"), "/run/user/1000")
	if err != nil {
		t.Fatal(err)
	}
	if c.Socket.Path != "/run/user/1000/hubctl.sock" || c.Browser.ActiveTab != "home" || c.Browser.ScreensaverTab != "slideshow" {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestDecodeValidation(t *testing.T) {
	for _, tt := range []struct{ name, input, runtime, want string }{
		{"unknown key", "[display]\noutput='HDMI-A-1'\noutpt='oops'", "/run/user/1000", "decode TOML"},
		{"unknown table", "[display]\noutput='HDMI-A-1'\n[typo]\nenabled=true", "/run/user/1000", "decode TOML"},
		{"missing output", "", "/run/user/1000", "display.output"},
		{"blank output", "[display]\noutput='  '", "/run/user/1000", "display.output"},
		{"missing runtime", "[display]\noutput='HDMI-A-1'", "", "socket.path"},
		{"relative socket", "[socket]\npath='hub.sock'", "", "socket.path"},
		{"root socket", "[socket]\npath='/'", "", "socket.path"},
		{"duplicate mappings", "[display]\noutput='HDMI-A-1'\n[browser]\nactive_tab='slideshow'", "/run/user/1000", "different"},
		{"unknown tab", "[display]\noutput='HDMI-A-1'\n[browser]\nactive_tab='missing'", "/run/user/1000", "configured tab names"},
		{"old numeric mapping", "[browser]\nactive_tab=1", "/run/user/1000", "decode TOML"},
		{"malformed", "[display", "/run/user/1000", "decode TOML"},
		{"relative input", "[input]\ndevice='event4'", "/run/user/1000", "input.device"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Decode(strings.NewReader(tt.input), tt.runtime)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want error containing %q, got %v", tt.want, err)
			}
		})
	}
}

func TestExplicitConfigurationWithoutRuntime(t *testing.T) {
	c, err := Decode(strings.NewReader("[socket]\npath='/tmp/panel.sock'\n[display]\noutput='HDMI-A-2'\n[browser]\nactive_tab='dashboard'\nscreensaver_tab='photos'\n[[browser.tabs]]\nname='dashboard'\nurl='http://ha.local'\n[[browser.tabs]]\nname='photos'\nurl='http://photos.local'"), "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Socket.Path != "/tmp/panel.sock" || c.Display.Output != "HDMI-A-2" || c.Browser.ActiveTab != "dashboard" || c.Browser.ScreensaverTab != "photos" || len(c.Browser.Tabs) != 2 {
		t.Fatalf("unexpected config: %+v", c)
	}
}

func TestBrowserValidation(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*Browser)
		want   string
	}{
		{"remote endpoint", func(b *Browser) { b.Endpoint = "http://192.168.1.2:9222" }, "loopback"},
		{"endpoint path", func(b *Browser) { b.Endpoint = "http://127.0.0.1:9222/debug" }, "endpoint"},
		{"duplicate names", func(b *Browser) { b.Tabs[1].Name = b.Tabs[0].Name }, "unique"},
		{"duplicate URLs", func(b *Browser) { b.Tabs[1].URL = b.Tabs[0].URL + "/" }, "unique"},
		{"invalid URL", func(b *Browser) { b.Tabs[0].URL = "file:///tmp/page.html" }, "HTTP(S)"},
		{"query selector", func(b *Browser) { b.Tabs[0].URL += "?page=1" }, "query"},
		{"empty tabs", func(b *Browser) { b.Tabs = nil }, "configured tab names"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := Config{Socket: Socket{Path: "/tmp/hubctl-test.sock"}, Display: Display{Output: "HDMI-A-1"}, Browser: DefaultBrowser()}
			tt.change(&c.Browser)
			if err := c.Validate(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error: %v", err)
			}
		})
	}
}
