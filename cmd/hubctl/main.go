package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"hubctl/internal/config"
	"hubctl/internal/daemon"
	"hubctl/internal/ipc"
)

var version = "dev"

const usage = `Usage:
  hubctl daemon [--config PATH]
  hubctl config check [--config PATH]
  hubctl status [--config PATH] [--json]
  hubctl active|screensaver|display-off [--config PATH]
  hubctl version
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "hubctl:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return fmt.Errorf("a command is required")
	}
	command := args[0]
	args = args[1:]
	switch command {
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
		return nil
	case "version":
		if len(args) != 0 {
			return fmt.Errorf("version takes no arguments")
		}
		fmt.Fprintln(stdout, "hubctl", version)
		return nil
	case "config":
		if len(args) == 0 || args[0] != "check" {
			return fmt.Errorf("expected: hubctl config check [--config PATH]")
		}
		args = args[1:]
	case "daemon", "status", "active", "screensaver", "display-off":
	default:
		return fmt.Errorf("unknown command %q", command)
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("config", "", "configuration file (default: user config directory/hubctl/config.toml)")
	var asJSON bool
	if command == "status" {
		flags.BoolVar(&asJSON, "json", false, "print machine-readable status")
	}
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}
	if *path == "" {
		p, err := config.DefaultPath()
		if err != nil {
			return fmt.Errorf("locate config: %w", err)
		}
		*path = p
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if command == "config" {
		fmt.Fprintln(stdout, "Configuration valid:", *path)
		return nil
	}
	if command == "status" {
		status, err := ipc.GetStatus(ctx, cfg.Socket.Path)
		if err != nil {
			return err
		}
		if asJSON {
			return json.NewEncoder(stdout).Encode(status)
		}
		_, err = fmt.Fprintf(stdout, "Daemon: running\nVersion: %s\nUptime: %.1fs\nHardware control: %t\nRequested mode: %s\nApplied mode: %s\nTransitioning: %t\n", status.Version, status.UptimeSeconds, status.HardwareControl, status.RequestedMode, status.AppliedMode, status.Transitioning)
		if err == nil && status.LastError != "" {
			_, err = fmt.Fprintln(stdout, "Last error:", status.LastError)
		}
		if err == nil && status.HardwareError != "" {
			_, err = fmt.Fprintln(stdout, "Hardware unavailable:", status.HardwareError)
		}
		if err == nil && status.Input != nil {
			if status.Input.WakeDelayRemainingSeconds > 0 {
				_, err = fmt.Fprintf(stdout, "Wake touch delay: %.1fs remaining\n", status.Input.WakeDelayRemainingSeconds)
			}
			_, err = fmt.Fprintf(stdout, "Touch: enabled=%t grabbed=%t down=%t\n", status.Input.Enabled, status.Input.Grabbed, status.Input.TouchDown)
			if err == nil && status.Input.Error != "" {
				_, err = fmt.Fprintln(stdout, "Touch error:", status.Input.Error)
			}
		}
		if err == nil && status.Policy != nil {
			_, err = fmt.Fprintf(stdout, "Policy: %s (%s), idle %.0fs\n", status.Policy.Period, status.Policy.Timezone, status.Policy.IdleSeconds)
			if err == nil && status.Policy.LastError != "" {
				_, err = fmt.Fprintln(stdout, "Policy error:", status.Policy.LastError)
			}
		}
		return err
	}
	if command != "daemon" {
		_, err := ipc.Execute(ctx, cfg.Socket.Path, command)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(stdout, "Applied mode:", command)
		return err
	}
	return daemon.Run(ctx, cfg, version, slog.New(slog.NewTextHandler(stderr, nil)))
}
