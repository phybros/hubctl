// Package daemon owns the service lifecycle.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"hubctl/internal/config"
	"hubctl/internal/controller"
	"hubctl/internal/input"
	"hubctl/internal/ipc"
	"hubctl/internal/policy"
)

// Run serves local control requests until cancellation.
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
	display, browser, hardwareErr := hardware(cfg)
	control := controller.New(display, browser)
	var actions interface {
		Apply(context.Context, controller.Mode) error
		Wake(context.Context) error
	} = control
	var automation *policy.Engine
	if cfg.Policy.Enabled && hardwareErr != nil {
		return fmt.Errorf("policy requires working hardware adapters: %w", hardwareErr)
	}
	var touch *input.Monitor
	if cfg.Input.Device != "" {
		touch, err = input.Open(cfg.Input.Device)
		if err != nil {
			return fmt.Errorf("initialize touch monitoring: %w", err)
		}
		control.SetInput(touch)
		delay, _ := cfg.Input.WakeDuration() // Already validated above.
		touch.SetWakeDelay(delay)
		if cfg.Policy.Enabled {
			automation, err = policy.New(cfg.Policy, control, touch.Snapshot, time.Now)
			if err != nil {
				touch.Close()
				return err
			}
			actions = automation
		}
		inputCtx, cancel := context.WithCancel(ctx)
		var workers sync.WaitGroup
		workers.Add(2)
		go func() { defer workers.Done(); touch.Run(inputCtx) }()
		go func() {
			defer workers.Done()
			for {
				select {
				case <-inputCtx.Done():
					return
				case <-touch.Wakes():
					wakeCtx, stop := context.WithTimeout(inputCtx, 2*time.Second)
					err := actions.Wake(wakeCtx)
					stop()
					if err != nil {
						logger.Warn("touch wake failed", "error", err)
					}
				}
			}
		}()
		defer func() { cancel(); workers.Wait(); touch.Close() }()
		logger.Info("touch monitoring started", "device", cfg.Input.Device)
	}
	if automation != nil {
		policyCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				tickCtx, stop := context.WithTimeout(policyCtx, 2*time.Second)
				err := automation.Tick(tickCtx)
				stop()
				if err != nil && policyCtx.Err() == nil {
					logger.Warn("automatic transition failed", "error", err)
				}
				select {
				case <-policyCtx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
		defer func() { cancel(); <-done }()
		logger.Info("local policy enabled", "night_start", cfg.Policy.NightStart, "night_end", cfg.Policy.NightEnd, "timezone", cfg.Policy.Timezone)
	}
	if hardwareErr != nil {
		logger.Warn("hardware control unavailable", "reason", hardwareErr)
	}
	logger.Info("daemon started", "socket_path", cfg.Socket.Path, "display_output", cfg.Display.Output, "hardware_control", control.Available())
	err = server.Serve(ctx, func() ipc.Status {
		status := ipc.Status{State: control.Snapshot(), Version: version, UptimeSeconds: time.Since(started).Seconds(), HardwareControl: control.Available()}
		if automation != nil {
			state := automation.Snapshot()
			status.Policy = &state
		}
		if touch != nil {
			state := touch.Snapshot()
			status.Input = &state
		}
		if hardwareErr != nil {
			status.HardwareError = hardwareErr.Error()
		}
		return status
	}, func(ctx context.Context, command string) *ipc.Error {
		if err := actions.Apply(ctx, controller.Mode(command)); err != nil {
			code := "transition_failed"
			if errors.Is(err, controller.ErrUnavailable) {
				code = "hardware_unavailable"
				if hardwareErr != nil {
					err = hardwareErr
				}
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
