package ipc

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func socketPath(t *testing.T) string {
	t.Helper()
	// Keep below macOS's short Unix socket path limit.
	dir, err := os.MkdirTemp("/tmp", "hubctl-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "hub.sock")
}

func TestServerLifecycle(t *testing.T) {
	path := socketPath(t)
	server, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("permissions: %v", info.Mode())
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(ctx, func() Status { return Status{Version: "test", UptimeSeconds: 42} }, nil)
	}()
	if other, err := Listen(path); err == nil {
		other.Close()
		t.Fatal("second listener succeeded")
	}
	status, err := GetStatus(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if status.Version != "test" || status.UptimeSeconds != 42 || status.HardwareControl {
		t.Fatalf("status: %+v", status)
	}
	for _, tt := range []struct{ frame, code string }{
		{"not json\n", "invalid_request"},
		{"{\"version\":2,\"command\":\"status\"}\n", "unsupported_version"},
		{"{\"version\":1,\"command\":\"reboot\"}\n", "unknown_command"},
		{strings.Repeat("x", maxFrame) + "\n", "invalid_request"},
	} {
		conn, err := net.Dial("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		conn.SetDeadline(time.Now().Add(time.Second))
		if _, err := conn.Write([]byte(tt.frame)); err != nil && len(tt.frame) <= maxFrame {
			conn.Close()
			t.Fatal(err)
		}
		var response Response
		err = json.NewDecoder(conn).Decode(&response)
		conn.Close()
		if err != nil {
			t.Fatal(err)
		}
		if response.Error == nil || response.Error.Code != tt.code {
			t.Fatalf("response: %+v", response)
		}
	}
	// A client that sends nothing must not hold up shutdown.
	idle, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown blocked")
	}
	server.Close()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("socket remains: %v", err)
	}
}

func TestExistingFilePreserved(t *testing.T) {
	path := socketPath(t)
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if server, err := Listen(path); err == nil {
		server.Close()
		t.Fatal("replaced file")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "keep" {
		t.Fatalf("file changed: %q %v", data, err)
	}
}

func TestUnavailableDaemon(t *testing.T) {
	_, err := GetStatus(context.Background(), socketPath(t))
	if err == nil || !strings.Contains(err.Error(), "is hubctl daemon running?") {
		t.Fatalf("error: %v", err)
	}
}

func TestUnresponsiveDaemon(t *testing.T) {
	l, err := net.Listen("unix", socketPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := l.Accept()
		if err == nil {
			defer conn.Close()
			var b [1024]byte
			for {
				if _, err := conn.Read(b[:]); err != nil {
					return
				}
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := GetStatus(ctx, l.Addr().String()); err == nil {
		t.Fatal("expected timeout")
	}
	if time.Since(started) > time.Second {
		t.Fatal("client ignored deadline")
	}
	l.Close()
	<-done
}
