// Package display controls a Wayland output with wlr-randr.
package display

import (
	"context"
	"hubctl/internal/command"
)

type WLR struct {
	Runner     command.Runner
	Executable string
	Output     string
}

func (d WLR) SetPower(ctx context.Context, on bool) error {
	state := "--off"
	if on {
		state = "--on"
	}
	return d.Runner.Run(ctx, d.Executable, "--output", d.Output, state)
}
