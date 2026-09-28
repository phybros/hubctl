package controller_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"hubctl/internal/controller"
	"hubctl/internal/display"
)

type recorder struct {
	calls [][]string
	fail  string
}

type fakeBrowser struct{ runner *recorder }

func (b fakeBrowser) Select(ctx context.Context, mode controller.Mode) error {
	return b.runner.Run(ctx, "browser", string(mode))
}

func (r *recorder) Run(_ context.Context, name string, args ...string) error {
	r.calls = append(r.calls, append([]string{name}, args...))
	if name == r.fail {
		return errors.New("tool failed")
	}
	return nil
}

func TestHardwareCommands(t *testing.T) {
	r := &recorder{}
	b := fakeBrowser{runner: r}
	c := controller.New(display.WLR{Runner: r, Executable: "/usr/bin/wlr-randr", Output: "HDMI-A-2"}, b)
	for _, mode := range []controller.Mode{controller.Active, controller.Screensaver, controller.DisplayOff} {
		if err := c.Apply(context.Background(), mode); err != nil {
			t.Fatal(err)
		}
	}
	want := [][]string{
		{"/usr/bin/wlr-randr", "--output", "HDMI-A-2", "--on"},
		{"browser", "active"},
		{"/usr/bin/wlr-randr", "--output", "HDMI-A-2", "--on"},
		{"browser", "screensaver"},
		{"/usr/bin/wlr-randr", "--output", "HDMI-A-2", "--off"},
	}
	if !reflect.DeepEqual(r.calls, want) {
		t.Fatalf("commands: %v", r.calls)
	}
	r.calls = nil
	r.fail = "/usr/bin/wlr-randr"
	if err := c.Apply(context.Background(), controller.Active); err == nil {
		t.Fatal("expected display failure")
	}
	if len(r.calls) != 1 {
		t.Fatal("browser invoked after display failure")
	}
	r.fail = "browser"
	if err := c.Apply(context.Background(), controller.Active); err == nil {
		t.Fatal("expected browser failure")
	}
	if c.Snapshot().AppliedMode != controller.Unknown {
		t.Fatal("partial failure reported success")
	}
}
