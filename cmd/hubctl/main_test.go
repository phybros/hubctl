package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hubctl/internal/controller"
	"hubctl/internal/ipc"
)

func TestCommands(t *testing.T) {
	for _, tt := range []struct {
		args      []string
		wantError bool
		want      string
	}{
		{[]string{"version"}, false, "hubctl dev"},
		{[]string{"--help"}, false, "Usage:"},
		{[]string{"daemon", "--help"}, false, "configuration file"},
		{nil, true, ""},
		{[]string{"nope"}, true, ""},
		{[]string{"version", "extra"}, true, ""},
		{[]string{"config"}, true, ""},
		{[]string{"daemon", "extra"}, true, ""},
		{[]string{"daemon", "--unknown"}, true, ""},
		{[]string{"config", "check", "--config", filepath.Join(t.TempDir(), "missing.toml")}, true, ""},
	} {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			var output bytes.Buffer
			err := run(context.Background(), tt.args, &output, &output)
			if (err != nil) != tt.wantError {
				t.Fatalf("unexpected error: %v", err)
			}
			if !strings.Contains(output.String(), tt.want) {
				t.Fatalf("unexpected output: %s", &output)
			}
		})
	}
}

func TestConfigCheckAndDaemonShutdown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	socketDir, err := os.MkdirTemp("/tmp", "hubctl-cli-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(socketDir)
	if err := os.WriteFile(path, []byte(fmt.Sprintf("[socket]\npath=%q\n[display]\noutput='HDMI-A-1'", filepath.Join(socketDir, "hub.sock"))), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run(context.Background(), []string{"config", "check", "--config", path}, &output, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Configuration valid") {
		t.Fatal(output.String())
	}
	output.Reset()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, []string{"daemon", "--config", path}, &output, &output) }()
	deadline := time.Now().Add(time.Second)
	for {
		_, err := ipc.GetStatus(ctx, filepath.Join(socketDir, "hub.sock"))
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon never became ready: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	var statusOutput bytes.Buffer
	if err := run(ctx, []string{"status", "--config", path, "--json"}, &statusOutput, &statusOutput); err != nil {
		t.Fatal(err)
	}
	var status ipc.Status
	if err := json.Unmarshal(statusOutput.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Version != version || status.HardwareControl || status.UptimeSeconds < 0 {
		t.Fatalf("unexpected status: %+v", status)
	}
	statusOutput.Reset()
	if err := run(ctx, []string{"status", "--config", path}, &statusOutput, &statusOutput); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(statusOutput.String(), "Daemon: running") {
		t.Fatal(statusOutput.String())
	}
	for _, mode := range []string{"active", "screensaver", "display-off"} {
		statusOutput.Reset()
		err := run(ctx, []string{mode, "--config", path}, &statusOutput, &statusOutput)
		if err == nil || !strings.Contains(err.Error(), "hardware_unavailable") {
			t.Fatalf("%s: expected unavailable hardware, got %v", mode, err)
		}
		if statusOutput.Len() != 0 {
			t.Fatalf("command reported success: %s", &statusOutput)
		}
		state, err := ipc.GetStatus(ctx, filepath.Join(socketDir, "hub.sock"))
		if err != nil {
			t.Fatal(err)
		}
		if state.RequestedMode != controller.Mode(mode) || state.AppliedMode != controller.Unknown || state.LastError == "" {
			t.Fatalf("state: %+v", state)
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not stop after cancellation")
	}
	if !strings.Contains(output.String(), "daemon stopped") {
		t.Fatal(output.String())
	}
}
