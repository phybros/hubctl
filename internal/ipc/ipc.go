// Package ipc implements the versioned, newline-delimited JSON control protocol.
// Each connection carries one request and one response.
package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"hubctl/internal/controller"
)

const ProtocolVersion = 1
const Timeout = 3 * time.Second
const maxFrame = 16 * 1024

type Request struct {
	Version int    `json:"version"`
	Command string `json:"command"`
}

type Status struct {
	controller.State
	Version         string  `json:"version"`
	UptimeSeconds   float64 `json:"uptime_seconds"`
	HardwareControl bool    `json:"hardware_control"`
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Response struct {
	Version int     `json:"version"`
	Status  *Status `json:"status,omitempty"`
	Error   *Error  `json:"error,omitempty"`
}

type Server struct {
	listener *net.UnixListener
	path     string
	info     os.FileInfo
	once     sync.Once
	closeErr error
}

// Listen never removes an existing path, including a stale socket. This prevents
// a second daemon from disrupting the owner of that path.
func Listen(path string) (*Server, error) {
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen on %s (existing paths are never replaced): %w", path, err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		l.Close()
		return nil, fmt.Errorf("secure socket: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		l.Close()
		return nil, err
	}
	l.SetUnlinkOnClose(false)
	return &Server{listener: l, path: path, info: info}, nil
}

func (s *Server) Close() error {
	s.once.Do(func() {
		s.closeErr = s.listener.Close()
		if info, err := os.Lstat(s.path); err == nil && os.SameFile(s.info, info) {
			if err := os.Remove(s.path); s.closeErr == nil {
				s.closeErr = err
			}
		}
	})
	return s.closeErr
}

func (s *Server) Serve(ctx context.Context, status func() Status, command func(context.Context, string) *Error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { s.Close() })
	defer stop()
	var wg sync.WaitGroup
	defer wg.Wait()
	slots := make(chan struct{}, 32)
	for {
		conn, err := s.listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			cancel()
			return fmt.Errorf("accept connection: %w", err)
		}
		select {
		case slots <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			defer conn.Close()
			stop := context.AfterFunc(ctx, func() { conn.Close() })
			defer stop()
			conn.SetDeadline(time.Now().Add(Timeout))
			response := Response{Version: ProtocolVersion}
			var request Request
			if err := readFrame(conn, &request); err != nil {
				response.Error = &Error{Code: "invalid_request", Message: "expected a newline-terminated JSON request"}
			} else if request.Version != ProtocolVersion {
				response.Error = &Error{Code: "unsupported_version", Message: "expected protocol version 1"}
			} else if request.Command != "status" && (!controller.Mode(request.Command).Valid() || command == nil) {
				response.Error = &Error{Code: "unknown_command", Message: "unknown command"}
			} else {
				if request.Command != "status" {
					requestCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
					response.Error = command(requestCtx, request.Command)
					cancel()
				}
				value := status()
				response.Status = &value
			}
			json.NewEncoder(conn).Encode(response)
		}()
	}
}

func readFrame(conn net.Conn, value any) error {
	frame, err := bufio.NewReaderSize(conn, maxFrame).ReadSlice('\n')
	if err != nil {
		return err
	}
	return json.Unmarshal(frame, value)
}

func GetStatus(ctx context.Context, path string) (Status, error) {
	return Execute(ctx, path, "status")
}

func Execute(ctx context.Context, path, command string) (Status, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return Status{}, fmt.Errorf("connect to daemon at %s (is hubctl daemon running?): %w", path, err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	if err := json.NewEncoder(conn).Encode(Request{Version: ProtocolVersion, Command: command}); err != nil {
		return Status{}, fmt.Errorf("send %s request: %w", command, err)
	}
	var response Response
	if err := readFrame(conn, &response); err != nil {
		return Status{}, fmt.Errorf("read %s response (command outcome may be unknown): %w", command, err)
	}
	if response.Version != ProtocolVersion {
		return Status{}, fmt.Errorf("unsupported response protocol version %d", response.Version)
	}
	if response.Error != nil {
		return Status{}, fmt.Errorf("daemon error %s: %s", response.Error.Code, response.Error.Message)
	}
	if response.Status == nil {
		return Status{}, fmt.Errorf("daemon response is missing status")
	}
	return *response.Status, nil
}
