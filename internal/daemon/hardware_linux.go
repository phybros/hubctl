package daemon

import (
	"fmt"
	"hubctl/internal/browser"
	"hubctl/internal/command"
	"hubctl/internal/config"
	"hubctl/internal/controller"
	"hubctl/internal/display"
	"os"
	"os/exec"
)

func hardware(cfg config.Config) (controller.Display, controller.Browser, error) {
	if os.Getenv("WAYLAND_DISPLAY") == "" || os.Getenv("XDG_RUNTIME_DIR") == "" {
		return nil, nil, fmt.Errorf("start the daemon from a terminal in the Wayland desktop session (WAYLAND_DISPLAY and XDG_RUNTIME_DIR are required)")
	}
	wlr, err := exec.LookPath("wlr-randr")
	if err != nil {
		return nil, nil, fmt.Errorf("install wlr-randr: %w", err)
	}
	runner := command.Exec{}
	return display.WLR{Runner: runner, Executable: wlr, Output: cfg.Display.Output}, browser.New(cfg.Browser), nil
}
