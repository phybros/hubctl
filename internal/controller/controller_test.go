package controller

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type displayFunc func(context.Context, bool) error

func (f displayFunc) SetPower(ctx context.Context, on bool) error { return f(ctx, on) }

type browserFunc func(context.Context, Mode) error

func (f browserFunc) Select(ctx context.Context, m Mode) error { return f(ctx, m) }

func TestTransitionsAndRetry(t *testing.T) {
	var calls []string
	fail := false
	c := New(displayFunc(func(_ context.Context, on bool) error {
		if on {
			calls = append(calls, "on")
		} else {
			calls = append(calls, "off")
		}
		return nil
	}), browserFunc(func(_ context.Context, m Mode) error {
		calls = append(calls, string(m))
		if fail {
			return errors.New("browser unavailable")
		}
		return nil
	}))
	if len(calls) != 0 || c.Snapshot().AppliedMode != Unknown {
		t.Fatal("startup changed state")
	}
	for _, mode := range []Mode{Active, Screensaver, DisplayOff, Active, Active} {
		if err := c.Apply(context.Background(), mode); err != nil {
			t.Fatal(err)
		}
		if s := c.Snapshot(); s.AppliedMode != mode || s.RequestedMode != mode || s.Transitioning || s.LastError != "" {
			t.Fatalf("state: %+v", s)
		}
	}
	want := []string{"on", "active", "on", "screensaver", "off", "on", "active", "on", "active"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls: %v", calls)
	}
	fail = true
	if err := c.Apply(context.Background(), Screensaver); err == nil {
		t.Fatal("expected failure")
	}
	if s := c.Snapshot(); s.AppliedMode != Unknown || s.RequestedMode != Screensaver || s.LastError == "" || s.Transitioning {
		t.Fatalf("partial failure: %+v", s)
	}
	fail = false
	if err := c.Apply(context.Background(), Screensaver); err != nil {
		t.Fatal(err)
	}
	if c.Snapshot().LastError != "" {
		t.Fatal("retry did not clear error")
	}
}

func TestDisplayFailureAndUnavailable(t *testing.T) {
	c := New(displayFunc(func(context.Context, bool) error { return errors.New("display failed") }), browserFunc(func(context.Context, Mode) error { t.Fatal("browser called after display failure"); return nil }))
	if err := c.Apply(context.Background(), Active); err == nil {
		t.Fatal("expected failure")
	}
	c = New(nil, nil)
	if err := c.Apply(context.Background(), Active); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	before := c.Snapshot()
	if err := c.Apply(context.Background(), Mode("invalid")); err == nil {
		t.Fatal("invalid mode accepted")
	}
	if c.Snapshot() != before {
		t.Fatal("invalid command changed state")
	}
}

func TestSerializationStatusAndCancellation(t *testing.T) {
	entered := make(chan bool, 2)
	release := make(chan struct{})
	c := New(displayFunc(func(ctx context.Context, on bool) error {
		entered <- on
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}), browserFunc(func(context.Context, Mode) error { return nil }))
	done := make(chan error, 1)
	go func() { done <- c.Apply(context.Background(), Active) }()
	<-entered
	if s := c.Snapshot(); !s.Transitioning || s.RequestedMode != Active || s.AppliedMode != Unknown {
		t.Fatalf("in-flight state: %+v", s)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := c.Apply(ctx, DisplayOff); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued cancellation: %v", err)
	}
	select {
	case <-entered:
		t.Fatal("concurrent hardware call")
	default:
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := c.Apply(context.Background(), DisplayOff); err != nil {
		t.Fatal(err)
	}
	if on := <-entered; on {
		t.Fatal("expected display off")
	}
}

func TestTransitionTimeout(t *testing.T) {
	c := New(displayFunc(func(ctx context.Context, _ bool) error { <-ctx.Done(); return ctx.Err() }), browserFunc(func(context.Context, Mode) error { t.Fatal("browser called after timeout"); return nil }))
	if err := c.Apply(context.Background(), Active); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout: %v", err)
	}
	if s := c.Snapshot(); s.Transitioning || s.AppliedMode != Unknown || s.LastError == "" {
		t.Fatalf("state after timeout: %+v", s)
	}
}
