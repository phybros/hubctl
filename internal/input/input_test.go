package input

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"hubctl/internal/controller"
)

func TestWakeDelay(t *testing.T) {
	for _, from := range []controller.Mode{controller.DisplayOff, controller.Screensaver} {
		t.Run(string(from), func(t *testing.T) {
			m, d := monitor(t)
			now := time.Unix(1000, 0)
			m.now = func() time.Time { return now }
			m.SetWakeDelay(4 * time.Second)
			if err := m.Prepare(from); err != nil {
				t.Fatal(err)
			}
			if err := m.Complete(from, true); err != nil {
				t.Fatal(err)
			}
			if err := m.Prepare(controller.Active); err != nil {
				t.Fatal(err)
			}
			if err := m.Complete(controller.Active, true); err != nil {
				t.Fatal(err)
			}
			if from == controller.Screensaver {
				if d.grabbed {
					t.Fatal("ordinary screensaver wake was delayed")
				}
				return
			}
			if !d.grabbed || m.Snapshot().WakeDelayRemainingSeconds != 4 {
				t.Fatal("off wake has no delay")
			}
			now = now.Add(3 * time.Second)
			d.contact(true)
			tick(t, m)
			d.contact(false)
			tick(t, m)
			if !d.grabbed || m.WakePending() {
				t.Fatal("extra touch released input or queued wake")
			}
			if err := m.Prepare(controller.Active); err != nil {
				t.Fatal(err)
			}
			if err := m.Complete(controller.Active, true); err != nil {
				t.Fatal(err)
			}
			if m.Snapshot().WakeDelayRemainingSeconds != 1 {
				t.Fatal("repeated active restarted timer")
			}
			d.contact(true)
			tick(t, m)
			now = now.Add(time.Second)
			tick(t, m)
			if !d.grabbed {
				t.Fatal("held finger leaked at deadline")
			}
			d.contact(false)
			tick(t, m)
			if d.grabbed {
				t.Fatal("finger-up after deadline did not release")
			}
		})
	}
}

func TestWakeDelayRetryAndIntermediateScreensaver(t *testing.T) {
	m, d := monitor(t)
	now := time.Unix(1000, 0)
	m.now = func() time.Time { return now }
	m.SetWakeDelay(4 * time.Second)
	if err := m.Prepare(controller.DisplayOff); err != nil {
		t.Fatal(err)
	}
	if err := m.Complete(controller.DisplayOff, true); err != nil {
		t.Fatal(err)
	}
	if err := m.Prepare(controller.Active); err != nil {
		t.Fatal(err)
	}
	if err := m.Complete(controller.Active, false); err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Second)
	tick(t, m)
	if !d.grabbed {
		t.Fatal("failed wake released input")
	}
	if err := m.Prepare(controller.Screensaver); err != nil {
		t.Fatal(err)
	}
	if err := m.Complete(controller.Screensaver, true); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if err := m.Prepare(controller.Active); err != nil {
		t.Fatal(err)
	}
	if err := m.Complete(controller.Active, true); err != nil {
		t.Fatal(err)
	}
	if m.Snapshot().WakeDelayRemainingSeconds != 3 {
		t.Fatal("intermediate screensaver bypassed delay")
	}
	now = now.Add(3 * time.Second)
	tick(t, m)
	if d.grabbed {
		t.Fatal("deadline with no fingers did not release")
	}
	if err := m.Prepare(controller.DisplayOff); err != nil {
		t.Fatal(err)
	}
	if err := m.Complete(controller.DisplayOff, true); err != nil {
		t.Fatal(err)
	}
	if err := m.Prepare(controller.Active); err != nil {
		t.Fatal(err)
	}
	if err := m.Complete(controller.Active, true); err != nil {
		t.Fatal(err)
	}
	if m.Snapshot().WakeDelayRemainingSeconds != 4 {
		t.Fatal("new off cycle did not reset delay")
	}
}

type displayAction func(context.Context, bool) error

func (f displayAction) SetPower(ctx context.Context, on bool) error { return f(ctx, on) }

type browserAction func(context.Context, controller.Mode) error

func (f browserAction) Select(ctx context.Context, mode controller.Mode) error { return f(ctx, mode) }

func TestControllerIntegration(t *testing.T) {
	m, d := monitor(t)
	var calls []string
	failBrowser := false
	c := controller.New(displayAction(func(_ context.Context, on bool) error {
		if !d.grabbed {
			t.Error("hardware changed before input capture")
		}
		if on {
			calls = append(calls, "on")
		} else {
			calls = append(calls, "off")
		}
		return nil
	}), browserAction(func(_ context.Context, mode controller.Mode) error {
		if !d.grabbed {
			t.Error("browser changed before input capture")
		}
		calls = append(calls, string(mode))
		if failBrowser {
			return errors.New("browser unavailable")
		}
		return nil
	}))
	c.SetInput(m)
	d.grabErr = errors.New("busy")
	if err := c.Apply(context.Background(), controller.DisplayOff); err == nil {
		t.Fatal("capture failure accepted")
	}
	if len(calls) != 0 || c.Snapshot().AppliedMode != controller.Unknown {
		t.Fatal("capture failure altered hardware/state")
	}
	d.grabErr = nil
	if err := c.Apply(context.Background(), controller.DisplayOff); err != nil {
		t.Fatal(err)
	}
	d.contact(true)
	tick(t, m)
	failBrowser = true
	if err := c.Wake(context.Background()); err == nil {
		t.Fatal("browser failure accepted")
	}
	if !d.grabbed {
		t.Fatal("browser failure released input")
	}
	d.contact(false)
	tick(t, m)
	d.contact(true)
	tick(t, m)
	failBrowser = false
	if err := c.Wake(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !d.grabbed {
		t.Fatal("held touch leaked after successful wake")
	}
	// One multitouch slot ending isn't the aggregate end of contact.
	d.events = []event{{Type: evAbs, Code: 57, Value: -1}, {Type: evSyn, Code: synReport}}
	tick(t, m)
	if !d.grabbed {
		t.Fatal("released before all fingers lifted")
	}
	d.contact(false)
	tick(t, m)
	if d.grabbed {
		t.Fatal("capture not released")
	}
	if err := c.Wake(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"off", "on", "active", "on", "active"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls: %v", calls)
	}
}

func TestReadErrorPreservesCapture(t *testing.T) {
	m, d := monitor(t)
	if err := m.Prepare(controller.DisplayOff); err != nil {
		t.Fatal(err)
	}
	d.readErr = errors.New("device disconnected")
	if err := m.Complete(controller.Active, true); err == nil {
		t.Fatal("read error ignored")
	}
	if !d.grabbed || m.Snapshot().Error == "" {
		t.Fatal("read error did not preserve capture/report failure")
	}
	if err := m.Prepare(controller.Active); err == nil {
		t.Fatal("unhealthy device accepted command")
	}
}

type fakeDevice struct {
	events                       []event
	down                         bool
	grabbed                      bool
	grabErr, readErr, releaseErr error
	closed                       bool
}

func (d *fakeDevice) Read() ([]event, error) {
	events := d.events
	d.events = nil
	return events, d.readErr
}
func (d *fakeDevice) TouchDown() (bool, error) { return d.down, nil }
func (d *fakeDevice) Grab(on bool) error {
	if on && d.grabErr != nil {
		return d.grabErr
	}
	if !on && d.releaseErr != nil {
		return d.releaseErr
	}
	d.grabbed = on
	return nil
}
func (d *fakeDevice) Close() error { d.closed = true; d.grabbed = false; return nil }
func (d *fakeDevice) contact(down bool) {
	d.down = down
	var value int32
	if down {
		value = 1
	}
	d.events = append(d.events, event{Type: evKey, Code: btnTouch, Value: value}, event{Type: evSyn, Code: synReport})
}
func monitor(t *testing.T) (*Monitor, *fakeDevice) {
	t.Helper()
	d := &fakeDevice{}
	m, err := newMonitor(d)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m, d
}
func tick(t *testing.T, m *Monitor) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.drain(); err != nil {
		t.Fatal(err)
	}
	if err := m.release(); err != nil {
		t.Fatal(err)
	}
}

func TestWakeGesture(t *testing.T) {
	for _, mode := range []controller.Mode{controller.DisplayOff, controller.Screensaver} {
		t.Run(string(mode), func(t *testing.T) {
			m, d := monitor(t)
			if d.grabbed {
				t.Fatal("startup grabbed input")
			}
			if err := m.Prepare(mode); err != nil {
				t.Fatal(err)
			}
			if !d.grabbed {
				t.Fatal("input not grabbed before transition")
			}
			if err := m.Complete(mode, true); err != nil {
				t.Fatal(err)
			}
			d.contact(true)
			tick(t, m)
			if !m.WakePending() {
				t.Fatal("touch did not request wake")
			}
			if err := m.Prepare(controller.Active); err != nil {
				t.Fatal(err)
			}
			if err := m.Complete(controller.Active, true); err != nil {
				t.Fatal(err)
			}
			if !d.grabbed {
				t.Fatal("held gesture leaked")
			}
			d.contact(false)
			tick(t, m)
			if d.grabbed {
				t.Fatal("input not released after finger-up")
			}
			d.contact(true)
			tick(t, m)
			if m.WakePending() || m.Snapshot().LastActivity.IsZero() {
				t.Fatal("active touch handling incorrect")
			}
		})
	}
}

func TestFailureRetryAndManualWake(t *testing.T) {
	m, d := monitor(t)
	if err := m.Prepare(controller.DisplayOff); err != nil {
		t.Fatal(err)
	}
	d.contact(true)
	tick(t, m)
	d.contact(false)
	tick(t, m)
	if err := m.Prepare(controller.Active); err != nil {
		t.Fatal(err)
	}
	if err := m.Complete(controller.Active, false); err != nil {
		t.Fatal(err)
	}
	if !d.grabbed {
		t.Fatal("failed activation released input")
	}
	d.contact(true)
	tick(t, m)
	if !m.WakePending() {
		t.Fatal("cannot retry on next touch")
	}
	// A newer manual sleep command supersedes pending touch activity.
	if err := m.Prepare(controller.DisplayOff); err != nil {
		t.Fatal(err)
	}
	if m.WakePending() {
		t.Fatal("stale wake remained pending")
	}
	d.contact(false)
	tick(t, m)
	if err := m.Prepare(controller.Active); err != nil {
		t.Fatal(err)
	}
	if err := m.Complete(controller.Active, true); err != nil {
		t.Fatal(err)
	}
	if d.grabbed {
		t.Fatal("manual active did not release")
	}
}

func TestCaptureFailureAndExistingContact(t *testing.T) {
	m, d := monitor(t)
	d.grabErr = errors.New("busy")
	if err := m.Prepare(controller.DisplayOff); err == nil {
		t.Fatal("accepted grab failure")
	}
	d.grabErr = nil
	d.contact(true)
	if err := m.Prepare(controller.Screensaver); err == nil {
		t.Fatal("stole in-progress gesture")
	}
	if d.grabbed {
		t.Fatal("grabbed during existing gesture")
	}
}

func TestDroppedEventsAndFrameBoundary(t *testing.T) {
	m, d := monitor(t)
	if err := m.Prepare(controller.DisplayOff); err != nil {
		t.Fatal(err)
	}
	d.contact(true)
	tick(t, m)
	if err := m.Prepare(controller.Active); err != nil {
		t.Fatal(err)
	}
	if err := m.Complete(controller.Active, true); err != nil {
		t.Fatal(err)
	}
	d.events = []event{{Type: evSyn, Code: synDropped}, {Type: evKey, Code: btnTouch, Value: 0}}
	tick(t, m)
	if !d.grabbed {
		t.Fatal("released on stale dropped event")
	}
	d.events = []event{{Type: evSyn, Code: synReport}}
	tick(t, m)
	if !d.grabbed {
		t.Fatal("kernel still reports contact")
	}
	d.down = false
	d.events = []event{{Type: evKey, Code: btnTouch, Value: 0}}
	tick(t, m)
	if !d.grabbed {
		t.Fatal("released in middle of frame")
	}
	d.events = []event{{Type: evSyn, Code: synReport}}
	tick(t, m)
	if d.grabbed {
		t.Fatal("did not release at frame boundary")
	}
}

func TestReleaseFailureAndClose(t *testing.T) {
	m, d := monitor(t)
	if err := m.Prepare(controller.DisplayOff); err != nil {
		t.Fatal(err)
	}
	d.releaseErr = errors.New("ungrab failed")
	if err := m.Complete(controller.Active, true); err == nil {
		t.Fatal("release failure hidden")
	}
	if !d.grabbed || m.Snapshot().Error == "" {
		t.Fatal("missing failure state")
	}
	m.Close()
	m.Close()
	if d.grabbed || !d.closed {
		t.Fatal("close did not release device")
	}
}
