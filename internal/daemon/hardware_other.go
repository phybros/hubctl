//go:build !linux

package daemon

import (
	"fmt"
	"hubctl/internal/config"
	"hubctl/internal/controller"
)

func hardware(config.Config) (controller.Display, controller.Browser, error) {
	return nil, nil, fmt.Errorf("hardware control requires Linux with a Wayland desktop session")
}
