// Package controller owns panel transitions independently of command transports.
package controller

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

type Mode string

const (
	Unknown           Mode = "unknown"
	Active            Mode = "active"
	Screensaver       Mode = "screensaver"
	DisplayOff        Mode = "display-off"
	TransitionTimeout      = time.Second
)

func (m Mode) Valid() bool { return m == Active || m == Screensaver || m == DisplayOff }

var ErrUnavailable = errors.New("hardware control is unavailable: display and browser adapters are not configured")

// Adapters must honor context cancellation and return only once their action stops.
type Display interface {
	SetPower(context.Context, bool) error
}
type Browser interface {
	Select(context.Context, Mode) error
}

type State struct {
	RequestedMode Mode   `json:"requested_mode"`
	AppliedMode   Mode   `json:"applied_mode"`
	Transitioning bool   `json:"transitioning"`
	LastError     string `json:"last_error,omitempty"`
}

type Controller struct {
	display Display
	browser Browser
	gate    chan struct{}
	mu      sync.RWMutex
	state   State
}

func New(display Display, browser Browser) *Controller {
	return &Controller{display: display, browser: browser, gate: make(chan struct{}, 1), state: State{RequestedMode: Unknown, AppliedMode: Unknown}}
}

func (c *Controller) Available() bool { return c.display != nil && c.browser != nil }
func (c *Controller) Snapshot() State { c.mu.RLock(); defer c.mu.RUnlock(); return c.state }

// Apply serializes transitions while allowing status reads during hardware calls.
// Queueing counts against the caller's deadline. Repeated modes are reapplied.
func (c *Controller) Apply(ctx context.Context, mode Mode) error {
	if !mode.Valid() {
		return fmt.Errorf("invalid mode %q", mode)
	}
	select {
	case c.gate <- struct{}{}:
		defer func() { <-c.gate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.state.RequestedMode = mode
	c.state.Transitioning = true
	c.state.AppliedMode = Unknown
	c.state.LastError = ""
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, TransitionTimeout)
	defer cancel()
	err := c.apply(ctx, mode)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state.Transitioning = false
	if err != nil {
		c.state.LastError = err.Error()
	} else {
		c.state.AppliedMode = mode
	}
	return err
}

func (c *Controller) apply(ctx context.Context, mode Mode) error {
	if !c.Available() {
		return ErrUnavailable
	}
	if err := c.display.SetPower(ctx, mode != DisplayOff); err != nil {
		return fmt.Errorf("display power: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("display power: %w", err)
	}
	if mode != DisplayOff {
		if err := c.browser.Select(ctx, mode); err != nil {
			return fmt.Errorf("browser selection: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("browser selection: %w", err)
		}
	}
	return nil
}
