package command

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestHelperProcess(t *testing.T) {
	if os.Getenv("HUBCTL_TEST_HELPER") != "1" {
		return
	}
	switch os.Args[len(os.Args)-1] {
	case "fail":
		fmt.Fprintln(os.Stderr, "cannot connect to display")
		os.Exit(7)
	case "wait":
		time.Sleep(10 * time.Second)
	case "noisy":
		fmt.Fprint(os.Stderr, strings.Repeat("x", 10000))
		os.Exit(1)
	case "literal;$(false)":
		fmt.Fprint(os.Stdout, "ok")
	default:
		os.Exit(9)
	}
	os.Exit(0)
}

func TestExec(t *testing.T) {
	t.Setenv("HUBCTL_TEST_HELPER", "1")
	// Avoid the race runtime's normal one-second exit delay in helper processes.
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	runner := Exec{}
	run := func(ctx context.Context, mode string) error {
		return runner.Run(ctx, os.Args[0], "-test.run=^TestHelperProcess$", "--", mode)
	}
	if err := run(context.Background(), "literal;$(false)"); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), "fail"); err == nil || !strings.Contains(err.Error(), "exit status 7") || !strings.Contains(err.Error(), "cannot connect to display") {
		t.Fatalf("failure: %v", err)
	}
	if err := run(context.Background(), "noisy"); err == nil || len(err.Error()) > 2000 {
		t.Fatalf("unbounded error: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := run(ctx, "wait"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout: %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("process was not stopped promptly")
	}
	if err := runner.Run(context.Background(), "/nonexistent/hubctl-test-tool"); err == nil {
		t.Fatal("missing executable succeeded")
	}
}
