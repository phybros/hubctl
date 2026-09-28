// Package policy implements local idle and wall-clock day/night behavior.
// Commands, touch wake, and timer decisions share a cancellable action gate.
package policy

import (
	"context"
	"sync"
	"time"

	"hubctl/internal/config"
	"hubctl/internal/controller"
	"hubctl/internal/input"
)

type Panel interface {
	Apply(context.Context, controller.Mode) error
	Wake(context.Context) error
	Snapshot() controller.State
}

type Status struct {
	Enabled     bool            `json:"enabled"`
	Period      string          `json:"period"`
	Timezone    string          `json:"timezone"`
	IdleSeconds float64         `json:"idle_seconds"`
	PendingMode controller.Mode `json:"pending_mode,omitempty"`
	LastError   string          `json:"last_error,omitempty"`
}

type Engine struct {
	panel              Panel
	activity           func() input.Status
	now                func() time.Time
	location           *time.Location
	start, end         int
	dayIdle, nightIdle time.Duration
	gate               chan struct{}
	mu                 sync.Mutex
	status             Status
	initialized        bool
	lastActivity       time.Time
	retryAt            time.Time
	pendingIdle        bool
}

func New(cfg config.Policy, panel Panel, activity func() input.Status, now func() time.Time) (*Engine, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		return nil, err
	}
	day, _ := time.ParseDuration(cfg.DayIdle)
	night, _ := time.ParseDuration(cfg.NightIdle)
	minute := func(value string) int { t, _ := time.Parse("15:04", value); return t.Hour()*60 + t.Minute() }
	e := &Engine{panel: panel, activity: activity, now: now, location: loc, start: minute(cfg.NightStart), end: minute(cfg.NightEnd), dayIdle: day, nightIdle: night, gate: make(chan struct{}, 1), lastActivity: now()}
	e.status = Status{Enabled: true, Period: e.period(now()), Timezone: loc.String()}
	return e, nil
}

func (e *Engine) period(now time.Time) string {
	t := now.In(e.location)
	m := t.Hour()*60 + t.Minute()
	night := m >= e.start && m < e.end
	if e.start > e.end {
		night = m >= e.start || m < e.end
	}
	if night {
		return "night"
	}
	return "day"
}
func (e *Engine) Snapshot() Status { e.mu.Lock(); defer e.mu.Unlock(); return e.status }
func (e *Engine) enter(ctx context.Context) error {
	select {
	case e.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-e.gate
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Explicit commands supersede outstanding policy retries and reset idle time.
func (e *Engine) Apply(ctx context.Context, mode controller.Mode) error {
	if err := e.enter(ctx); err != nil {
		return err
	}
	defer func() { <-e.gate }()
	err := e.panel.Apply(ctx, mode)
	e.reset(err)
	return err
}
func (e *Engine) Wake(ctx context.Context) error {
	if err := e.enter(ctx); err != nil {
		return err
	}
	defer func() { <-e.gate }()
	err := e.panel.Wake(ctx)
	if err != nil || e.panel.Snapshot().AppliedMode == controller.Active {
		e.reset(err)
	}
	return err
}
func (e *Engine) reset(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.initialized = true
	e.lastActivity = e.now()
	e.retryAt = time.Time{}
	e.pendingIdle = false
	e.status.Period = e.period(e.now())
	e.status.IdleSeconds = 0
	e.status.PendingMode = ""
	e.status.LastError = ""
	if err != nil {
		e.status.LastError = err.Error()
	}
}

// Tick re-evaluates current wall time (including midnight, DST and clock changes),
// but measures idle durations with Go's monotonic timestamps when available.
func (e *Engine) Tick(ctx context.Context) error {
	if err := e.enter(ctx); err != nil {
		return err
	}
	defer func() { <-e.gate }()
	return e.tick(ctx)
}

// Configure commits durable settings while holding the same gate as commands
// and timer decisions. Hardware errors after acceptance remain in policy status.
func (e *Engine) Configure(ctx context.Context, cfg config.Policy, commit func() error) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := e.enter(ctx); err != nil {
		return err
	}
	defer func() { <-e.gate }()
	if err := commit(); err != nil {
		return err
	}
	e.dayIdle, _ = time.ParseDuration(cfg.DayIdle)
	e.nightIdle, _ = time.ParseDuration(cfg.NightIdle)
	start, _ := time.Parse("15:04", cfg.NightStart)
	end, _ := time.Parse("15:04", cfg.NightEnd)
	e.start, e.end = start.Hour()*60+start.Minute(), end.Hour()*60+end.Minute()
	// Recompute pending idle decisions against the new durations, without
	// discarding elapsed activity or a pending schedule boundary.
	e.mu.Lock()
	if e.pendingIdle {
		e.status.PendingMode = ""
		e.pendingIdle = false
		e.retryAt = time.Time{}
	}
	e.mu.Unlock()
	_ = e.tick(ctx)
	return nil
}

func (e *Engine) tick(ctx context.Context) error {
	now := e.now()
	touch := e.activity()
	panel := e.panel.Snapshot()
	e.mu.Lock()
	period := e.period(now)
	if !e.initialized || period != e.status.Period {
		e.initialized = true
		e.status.Period = period
		e.lastActivity = now
		e.retryAt = time.Time{}
		e.pendingIdle = false
		e.status.PendingMode = controller.Active
		if period == "night" {
			e.status.PendingMode = controller.DisplayOff
		}
	}
	active := touch.LastActivity.After(e.lastActivity) || touch.TouchDown || touch.WakeDelayRemainingSeconds > 0
	if touch.LastActivity.After(e.lastActivity) {
		e.lastActivity = touch.LastActivity
	}
	if touch.TouchDown || touch.WakeDelayRemainingSeconds > 0 {
		e.lastActivity = now
	}
	if active && e.pendingIdle {
		e.status.PendingMode = ""
		e.pendingIdle = false
		e.retryAt = time.Time{}
	}
	e.status.IdleSeconds = max(0, now.Sub(e.lastActivity).Seconds())
	if touch.Error != "" || !touch.Enabled {
		e.status.LastError = "touch activity unavailable; automatic transitions suspended"
		e.mu.Unlock()
		return nil
	}
	if e.status.PendingMode == "" {
		if period == "day" && panel.AppliedMode == controller.Active && now.Sub(e.lastActivity) >= e.dayIdle {
			e.status.PendingMode = controller.Screensaver
			e.pendingIdle = true
		}
		if period == "night" && (panel.AppliedMode == controller.Active || panel.AppliedMode == controller.Screensaver) && now.Sub(e.lastActivity) >= e.nightIdle {
			e.status.PendingMode = controller.DisplayOff
			e.pendingIdle = true
		}
	}
	mode := e.status.PendingMode
	if mode == "" || now.Before(e.retryAt) || (touch.TouchDown && mode != controller.Active) {
		e.mu.Unlock()
		return nil
	}
	e.mu.Unlock()
	err := e.panel.Apply(ctx, mode)
	e.mu.Lock()
	defer e.mu.Unlock()
	if err != nil {
		e.status.LastError = err.Error()
		e.retryAt = e.now().Add(5 * time.Second)
	} else {
		e.status.PendingMode = ""
		e.status.LastError = ""
		e.retryAt = time.Time{}
		e.lastActivity = e.now()
		e.status.IdleSeconds = 0
	}
	return err
}
