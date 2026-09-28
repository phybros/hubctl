// Package input monitors touch contact and captures wake gestures before they
// reach the desktop. The device must expose aggregate BTN_TOUCH contact state.
package input

import (
	"context"
	"fmt"
	"sync"
	"time"

	"hubctl/internal/controller"
)

type event struct {
	Type, Code uint16
	Value      int32
}

const (
	evSyn      = 0
	evKey      = 1
	evAbs      = 3
	synReport  = 0
	synDropped = 3
	btnTouch   = 330
)

// Read drains available events without blocking; TouchDown queries kernel state.
type device interface {
	Read() ([]event, error)
	TouchDown() (bool, error)
	Grab(bool) error
	Close() error
}

type Status struct {
	Enabled                   bool      `json:"enabled"`
	Grabbed                   bool      `json:"grabbed"`
	TouchDown                 bool      `json:"touch_down"`
	LastActivity              time.Time `json:"last_activity,omitempty"`
	Error                     string    `json:"error,omitempty"`
	WakeDelayRemainingSeconds float64   `json:"wake_delay_remaining_seconds"`
}

type Monitor struct {
	mu           sync.Mutex
	dev          device
	state        Status
	releaseReady bool
	pending      bool
	dropped      bool
	inFrame      bool
	wakes        chan struct{}
	closed       bool
	wakeDelay    time.Duration
	wakePending  bool
	releaseAfter time.Time
	now          func() time.Time
}

func newMonitor(dev device) (*Monitor, error) {
	down, err := dev.TouchDown()
	if err != nil {
		dev.Close()
		return nil, err
	}
	return &Monitor{dev: dev, state: Status{Enabled: true, TouchDown: down}, wakes: make(chan struct{}, 1), now: time.Now}, nil
}

func (m *Monitor) Snapshot() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.state
	if m.state.Grabbed && !m.releaseAfter.IsZero() {
		s.WakeDelayRemainingSeconds = max(0, m.releaseAfter.Sub(m.now()).Seconds())
	}
	return s
}

// SetWakeDelay must be called before starting the monitor or serving commands.
func (m *Monitor) SetWakeDelay(delay time.Duration) { m.wakeDelay = delay }
func (m *Monitor) Wakes() <-chan struct{}           { return m.wakes }

func (m *Monitor) fail(err error) error {
	if err != nil {
		m.state.Error = err.Error()
	}
	return err
}

func (m *Monitor) Prepare(mode controller.Mode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return fmt.Errorf("touch device is closed")
	}
	if m.state.Error != "" {
		return fmt.Errorf("touch monitor failed: %s", m.state.Error)
	}
	if err := m.drain(); err != nil {
		return m.fail(err)
	}
	if mode != controller.Active && !m.state.Grabbed {
		// Do not steal an in-progress gesture from the compositor.
		down, err := m.dev.TouchDown()
		if err != nil {
			return m.fail(err)
		}
		if down {
			return fmt.Errorf("lift all fingers before changing to %s", mode)
		}
		if err := m.dev.Grab(true); err != nil {
			return fmt.Errorf("grab touchscreen: %w", err)
		}
		m.state.Grabbed = true
		// Recheck after grab to cover a touch beginning during acquisition. If a
		// contact raced the grab, suppress it until lifted rather than releasing it.
		down, err = m.dev.TouchDown()
		if err != nil {
			return m.fail(err)
		}
		m.state.TouchDown = down
		if down {
			return fmt.Errorf("touch began while capturing input; lift all fingers and retry the command")
		}
	}
	m.pending = false
	m.releaseReady = false
	if mode == controller.DisplayOff {
		// Conservatively remember even failed off commands, which may have
		// partially changed the output before reporting an error.
		m.wakePending = true
		m.releaseAfter = time.Time{}
	}
	return nil
}

func (m *Monitor) Complete(mode controller.Mode, success bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Error != "" {
		return fmt.Errorf("touch monitor failed: %s", m.state.Error)
	}
	// Only a successful active transition permits release, including manual wake.
	if success && mode != controller.DisplayOff && m.wakePending {
		m.releaseAfter = m.now().Add(m.wakeDelay)
		m.wakePending = false
	}
	m.releaseReady = success && mode == controller.Active
	if err := m.drain(); err != nil {
		return m.fail(err)
	}
	return m.fail(m.release())
}

func (m *Monitor) WakePending() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pending && m.state.Grabbed && !m.releaseReady && m.state.Error == ""
}

func (m *Monitor) release() error {
	if m.now().Before(m.releaseAfter) {
		return nil
	}
	if !m.state.Grabbed || !m.releaseReady || m.dropped || m.inFrame || m.state.TouchDown {
		return nil
	}
	// A fresh state query prevents releasing on an old queued finger-up event.
	down, err := m.dev.TouchDown()
	if err != nil {
		return err
	}
	m.state.TouchDown = down
	if down {
		return nil
	}
	if err := m.dev.Grab(false); err != nil {
		return fmt.Errorf("ungrab touchscreen: %w", err)
	}
	m.state.Grabbed = false
	m.pending = false
	m.releaseAfter = time.Time{}
	return nil
}

func (m *Monitor) drain() error {
	events, err := m.dev.Read()
	if err != nil {
		return err
	}
	for _, e := range events {
		m.inFrame = !(e.Type == evSyn && e.Code == synReport)
		if e.Type == evSyn && e.Code == synDropped {
			m.dropped = true
			continue
		}
		if m.dropped {
			if e.Type == evSyn && e.Code == synReport {
				down, err := m.dev.TouchDown()
				if err != nil {
					return err
				}
				m.state.TouchDown = down
				m.dropped = false
				if down {
					m.activity(true)
				}
			}
			continue
		}
		if e.Type == evKey && e.Code == btnTouch {
			wasDown := m.state.TouchDown
			m.state.TouchDown = e.Value != 0
			m.activity(!wasDown && m.state.TouchDown)
		} else if e.Type == evAbs && m.state.TouchDown {
			m.activity(false)
		}
	}
	return nil
}

func (m *Monitor) activity(wake bool) {
	m.state.LastActivity = m.now()
	if wake && m.state.Grabbed && !m.releaseReady {
		m.pending = true
		select {
		case m.wakes <- struct{}{}:
		default:
		}
	}
}

// Run keeps capture on read failures, reporting the error through status. A
// restart is required after disconnect/errors; silently losing capture is unsafe.
func (m *Monitor) Run(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.mu.Lock()
			if m.closed || m.state.Error != "" {
				m.mu.Unlock()
				return
			}
			err := m.drain()
			if err == nil {
				err = m.release()
			}
			m.fail(err)
			m.mu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

func (m *Monitor) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	err := m.dev.Close() // Closing the descriptor releases EVIOCGRAB in the kernel.
	m.state.Grabbed = false
	m.state.Enabled = false
	return err
}
