package policy

import (
	"context"
	"errors"
	"testing"
	"time"

	"hubctl/internal/config"
	"hubctl/internal/controller"
	"hubctl/internal/input"
)

type fakePanel struct {
	mode  controller.Mode
	calls []controller.Mode
	fail  bool
}

func (p *fakePanel) Apply(_ context.Context, m controller.Mode) error {
	p.calls = append(p.calls, m)
	if p.fail {
		return errors.New("hardware failure")
	}
	p.mode = m
	return nil
}
func (p *fakePanel) Wake(ctx context.Context) error { return p.Apply(ctx, controller.Active) }
func (p *fakePanel) Snapshot() controller.State     { return controller.State{AppliedMode: p.mode} }

func setup(t *testing.T, stamp string) (*Engine, *fakePanel, *input.Status, *time.Time) {
	t.Helper()
	now, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		t.Fatal(err)
	}
	p := &fakePanel{mode: controller.Unknown}
	touch := &input.Status{Enabled: true}
	cfg := config.DefaultPolicy()
	cfg.Timezone = "UTC"
	e, err := New(cfg, p, func() input.Status { return *touch }, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return e, p, touch, &now
}
func tick(t *testing.T, e *Engine) {
	t.Helper()
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeConfiguration(t *testing.T) {
	e, p, touch, now := setup(t, "2026-09-26T12:00:00Z")
	tick(t, e)
	*now = now.Add(4 * time.Minute)
	cfg := config.DefaultPolicy()
	cfg.DayIdle = "3m"
	if err := e.Configure(context.Background(), cfg, func() error { return errors.New("disk full") }); err == nil {
		t.Fatal("commit failure ignored")
	}
	tick(t, e)
	if p.mode != controller.Active {
		t.Fatal("failed commit changed timeout")
	}
	if err := e.Configure(context.Background(), cfg, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if p.mode != controller.Screensaver {
		t.Fatal("shorter timeout lost elapsed idle")
	}
	// Moving into night behaves like a normal boundary, including touch safety.
	cfg.NightStart, cfg.NightEnd = "11:00", "13:00"
	touch.TouchDown = true
	if err := e.Configure(context.Background(), cfg, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if p.mode != controller.Screensaver || e.Snapshot().PendingMode != controller.DisplayOff {
		t.Fatal(e.Snapshot())
	}
	touch.TouchDown = false
	tick(t, e)
	if p.mode != controller.DisplayOff {
		t.Fatal(p.mode)
	}
	cfg.NightStart, cfg.NightEnd = "22:00", "07:00"
	if err := e.Configure(context.Background(), cfg, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if p.mode != controller.Active {
		t.Fatal("schedule edit did not enter day")
	}
}

func TestLongerTimeoutCancelsIdleRetry(t *testing.T) {
	e, p, _, now := setup(t, "2026-09-26T12:00:00Z")
	tick(t, e)
	*now = now.Add(5 * time.Minute)
	p.fail = true
	if err := e.Tick(context.Background()); err == nil {
		t.Fatal("expected hardware failure")
	}
	p.fail = false
	cfg := config.DefaultPolicy()
	cfg.DayIdle = "10m"
	if err := e.Configure(context.Background(), cfg, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if p.mode != controller.Active || e.Snapshot().PendingMode != "" {
		t.Fatal(e.Snapshot())
	}
	*now = now.Add(5 * time.Minute)
	tick(t, e)
	if p.mode != controller.Screensaver {
		t.Fatal("elapsed idle was reset")
	}
}

func TestDayIdleAndActivity(t *testing.T) {
	e, p, touch, now := setup(t, "2026-09-26T12:00:00Z")
	tick(t, e)
	if p.mode != controller.Active {
		t.Fatal("startup not active")
	}
	*now = now.Add(4 * time.Minute)
	tick(t, e)
	if len(p.calls) != 1 {
		t.Fatal("early idle")
	}
	touch.LastActivity = *now
	*now = now.Add(4 * time.Minute)
	tick(t, e)
	if p.mode != controller.Active {
		t.Fatal("touch did not reset idle")
	}
	*now = now.Add(time.Minute)
	tick(t, e)
	if p.mode != controller.Screensaver {
		t.Fatal("idle did not show screensaver")
	}
	*now = now.Add(time.Hour)
	tick(t, e)
	if len(p.calls) != 2 {
		t.Fatal("screensaver repeatedly reapplied")
	}
	if err := e.Wake(context.Background()); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(4 * time.Minute)
	tick(t, e)
	if p.mode != controller.Active {
		t.Fatal("wake did not reset timer")
	}
}

func TestNightWakeAndMorning(t *testing.T) {
	e, p, _, now := setup(t, "2026-09-26T21:59:59Z")
	tick(t, e)
	*now = now.Add(time.Second)
	tick(t, e)
	if p.mode != controller.DisplayOff || e.Snapshot().Period != "night" {
		t.Fatal("night boundary")
	}
	if err := e.Wake(context.Background()); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(119 * time.Second)
	tick(t, e)
	if p.mode != controller.Active {
		t.Fatal("night wake ended early")
	}
	*now = now.Add(time.Second)
	tick(t, e)
	if p.mode != controller.DisplayOff {
		t.Fatal("night timeout")
	}
	*now = time.Date(2026, 9, 27, 7, 0, 0, 0, time.UTC)
	tick(t, e)
	if p.mode != controller.Active || e.Snapshot().Period != "day" {
		t.Fatal("morning boundary")
	}
}

func TestNightStartupAndManualCommands(t *testing.T) {
	e, p, _, now := setup(t, "2026-09-26T01:00:00Z")
	tick(t, e)
	if p.mode != controller.DisplayOff {
		t.Fatal("night startup")
	}
	if err := e.Apply(context.Background(), controller.Screensaver); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Minute)
	if err := e.Apply(context.Background(), controller.Screensaver); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(119 * time.Second)
	tick(t, e)
	if p.mode != controller.Screensaver {
		t.Fatal("repeated manual command did not reset timer")
	}
	*now = now.Add(time.Second)
	tick(t, e)
	if p.mode != controller.DisplayOff {
		t.Fatal("manual screensaver did not expire at night")
	}
}

func TestHeldTouchAndWakeDelay(t *testing.T) {
	e, p, touch, now := setup(t, "2026-09-26T12:00:00Z")
	tick(t, e)
	touch.TouchDown = true
	*now = now.Add(10 * time.Minute)
	tick(t, e)
	if p.mode != controller.Active {
		t.Fatal("idled during held touch")
	}
	touch.TouchDown = false
	touch.LastActivity = *now
	touch.WakeDelayRemainingSeconds = 4
	*now = now.Add(10 * time.Minute)
	tick(t, e)
	if p.mode != controller.Active {
		t.Fatal("idled during wake delay")
	}
	touch.WakeDelayRemainingSeconds = 0
	*now = now.Add(5 * time.Minute)
	tick(t, e)
	if p.mode != controller.Screensaver {
		t.Fatal("idle never resumed")
	}
}

func TestRetryAndNewActivity(t *testing.T) {
	e, p, touch, now := setup(t, "2026-09-26T12:00:00Z")
	tick(t, e)
	p.fail = true
	*now = now.Add(5 * time.Minute)
	if err := e.Tick(context.Background()); err == nil {
		t.Fatal("missing failure")
	}
	tick(t, e)
	if len(p.calls) != 2 {
		t.Fatal("retry flooded hardware")
	}
	*now = now.Add(time.Second)
	touch.LastActivity = *now
	p.fail = false
	tick(t, e)
	if e.Snapshot().PendingMode != "" || len(p.calls) != 2 {
		t.Fatal("stale idle retry survived new activity")
	}
	*now = now.Add(5 * time.Minute)
	p.fail = true
	if err := e.Tick(context.Background()); err == nil {
		t.Fatal("missing retry failure")
	}
	p.fail = false
	*now = now.Add(5 * time.Second)
	tick(t, e)
	if p.mode != controller.Screensaver {
		t.Fatal("retry failed")
	}
}

func TestBoundaryHeldTouchAndUnavailableInput(t *testing.T) {
	e, p, touch, now := setup(t, "2026-09-26T21:59:00Z")
	tick(t, e)
	touch.TouchDown = true
	*now = now.Add(time.Minute)
	tick(t, e)
	if p.mode != controller.Active {
		t.Fatal("night interrupted held gesture")
	}
	touch.TouchDown = false
	touch.LastActivity = *now
	tick(t, e)
	if p.mode != controller.DisplayOff {
		t.Fatal("night boundary lost on finger-up")
	}
	touch.Error = "unplugged"
	*now = time.Date(2026, 9, 27, 7, 0, 0, 0, time.UTC)
	tick(t, e)
	if p.mode != controller.DisplayOff || e.Snapshot().LastError == "" {
		t.Fatal("input failure ignored")
	}
}

func TestScheduleWindowsAndTimezone(t *testing.T) {
	cfg := config.DefaultPolicy()
	cfg.NightStart = "09:00"
	cfg.NightEnd = "17:00"
	cfg.Timezone = "America/Toronto"
	e, err := New(cfg, &fakePanel{}, func() input.Status { return input.Status{Enabled: true} }, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ stamp, want string }{
		{"2026-09-26T12:59:00Z", "day"}, {"2026-09-26T13:00:00Z", "night"}, {"2026-09-26T21:00:00Z", "day"},
	} {
		at, _ := time.Parse(time.RFC3339, tt.stamp)
		if got := e.period(at); got != tt.want {
			t.Fatalf("%s: %s", tt.stamp, got)
		}
	}
	cfg = config.DefaultPolicy()
	cfg.Timezone = "America/Toronto"
	e, err = New(cfg, &fakePanel{}, func() input.Status { return input.Status{Enabled: true} }, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	// Both occurrences of 01:30 during the autumn DST change remain night.
	for _, stamp := range []string{"2026-11-01T05:30:00Z", "2026-11-01T06:30:00Z"} {
		at, _ := time.Parse(time.RFC3339, stamp)
		if e.period(at) != "night" {
			t.Fatal("DST changed night period")
		}
	}
}

func TestManualSupersedesBoundaryRetry(t *testing.T) {
	e, p, _, now := setup(t, "2026-09-26T22:00:00Z")
	p.fail = true
	if err := e.Tick(context.Background()); err == nil {
		t.Fatal("missing failure")
	}
	p.fail = false
	if err := e.Apply(context.Background(), controller.Active); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(5 * time.Second)
	tick(t, e)
	if p.mode != controller.Active || len(p.calls) != 2 {
		t.Fatal("stale boundary retry overrode manual active")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.Apply(ctx, controller.DisplayOff); !errors.Is(err, context.Canceled) {
		t.Fatalf("error: %v", err)
	}
	if len(p.calls) != 2 {
		t.Fatal("cancelled command reached panel")
	}
}
