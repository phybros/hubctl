// Package daemon owns the service lifecycle.
package daemon

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"hubctl/internal/config"
	"hubctl/internal/controller"
	"hubctl/internal/ipc"
)

// Run serves local control requests until cancellation. Hardware is not controlled yet.
func Run(ctx context.Context, cfg config.Config, version string, logger *slog.Logger) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	server, err := ipc.Listen(cfg.Socket.Path)
	if err != nil {
		return err
	}
	defer server.Close()
	started := time.Now()
	control := controller.New(nil, nil)
	logger.Info("daemon started", "socket_path", cfg.Socket.Path, "display_output", cfg.Display.Output, "hardware_control", false)
	err = server.Serve(ctx, func() ipc.Status {
		return ipc.Status{State: control.Snapshot(), Version: version, UptimeSeconds: time.Since(started).Seconds(), HardwareControl: control.Available()}
	}, func(ctx context.Context, command string) *ipc.Error {
		if err := control.Apply(ctx, controller.Mode(command)); err != nil {
			code := "transition_failed"
			if errors.Is(err, controller.ErrUnavailable) {
				code = "hardware_unavailable"
			}
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				code = "transition_interrupted"
			}
			logger.Warn("mode command failed", "mode", command, "error", err)
			return &ipc.Error{Code: code, Message: err.Error()}
		}
		return nil
	})
	logger.Info("daemon stopped")
	return err
}
