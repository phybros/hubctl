package config

import (
	"strings"
	"testing"
)

func TestDecodeDefaults(t *testing.T) {
	c, err := Decode(strings.NewReader("[display]\noutput = 'HDMI-A-1'\n"), "/run/user/1000")
	if err != nil {
		t.Fatal(err)
	}
	if c.Socket.Path != "/run/user/1000/hubctl.sock" || c.Browser.ActiveTab != 1 || c.Browser.ScreensaverTab != 2 {
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
		{"duplicate tabs", "[display]\noutput='HDMI-A-1'\n[browser]\nactive_tab=2", "/run/user/1000", "different"},
		{"last tab shortcut", "[display]\noutput='HDMI-A-1'\n[browser]\nscreensaver_tab=9", "/run/user/1000", "between 1 and 8"},
		{"zero tab", "[display]\noutput='HDMI-A-1'\n[browser]\nactive_tab=0", "/run/user/1000", "between 1 and 8"},
		{"wrong type", "[browser]\nactive_tab='one'", "/run/user/1000", "decode TOML"},
		{"malformed", "[display", "/run/user/1000", "decode TOML"},
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
	c, err := Decode(strings.NewReader("[socket]\npath='/tmp/panel.sock'\n[display]\noutput='HDMI-A-2'\n[browser]\nactive_tab=3\nscreensaver_tab=4"), "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Socket.Path != "/tmp/panel.sock" || c.Display.Output != "HDMI-A-2" || c.Browser.ActiveTab != 3 || c.Browser.ScreensaverTab != 4 {
		t.Fatalf("unexpected config: %+v", c)
	}
}
