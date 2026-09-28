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
	"hubctl/internal/settings"
)

var version = "dev"

const usage = `Usage:
  hubctl daemon [--config PATH]
  hubctl config check [--config PATH]
  hubctl status [--config PATH] [--json]
  hubctl active|screensaver|display-off [--config PATH]
  hubctl settings list [--config PATH] [--json]
  hubctl settings set NAME VALUE [--config PATH]
  hubctl settings reset NAME|all [--config PATH]
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
	var settingAction, settingName, settingValue string
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
	case "settings":
		if len(args) == 0 {
			return fmt.Errorf("expected settings list, set NAME VALUE, or reset NAME|all")
		}
		settingAction, args = args[0], args[1:]
		switch settingAction {
		case "list":
		case "set":
			if len(args) < 2 {
				return fmt.Errorf("expected settings set NAME VALUE")
			}
			settingName, settingValue, args = args[0], args[1], args[2:]
		case "reset":
			if len(args) < 1 {
				return fmt.Errorf("expected settings reset NAME|all")
			}
			settingName, args = args[0], args[1:]
		default:
			return fmt.Errorf("unknown settings action %q", settingAction)
		}
	default:
		return fmt.Errorf("unknown command %q", command)
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("config", "", "configuration file (default: user config directory/hubctl/config.toml)")
	var asJSON bool
	if command == "status" || command == "settings" {
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
	if command == "settings" {
		request := ipc.Request{Version: ipc.ProtocolVersion, Command: "status"}
		if settingAction != "list" {
			request.Command = "settings." + settingAction
			request.Name = settingName
			request.Value = settingValue
		}
		status, err := ipc.Send(ctx, cfg.Socket.Path, request)
		if err != nil {
			return err
		}
		if status.Settings == nil {
			return fmt.Errorf("daemon does not expose runtime settings; update and restart it")
		}
		if asJSON {
			return json.NewEncoder(stdout).Encode(status.Settings)
		}
		return printSettings(stdout, status.Settings)
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
		if err == nil && status.MQTT != nil {
			_, err = fmt.Fprintf(stdout, "MQTT: connected=%t\n", status.MQTT.Connected)
			if err == nil && status.MQTT.LastError != "" {
				_, err = fmt.Fprintln(stdout, "MQTT error:", status.MQTT.LastError)
			}
			if err == nil && status.MQTT.LastCommandError != "" {
				_, err = fmt.Fprintln(stdout, "MQTT command error:", status.MQTT.LastCommandError)
			}
		}
		if err == nil && status.Settings != nil {
			err = printSettings(stdout, status.Settings)
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

func printSettings(w io.Writer, snapshot *settings.Snapshot) error {
	if _, err := fmt.Fprintf(w, "Settings: %s (policy enabled=%t)\n", snapshot.Path, snapshot.PolicyEnabled); err != nil {
		return err
	}
	for _, name := range settings.Names {
		v := snapshot.Values[name]
		if _, err := fmt.Fprintf(w, "  %s: %s [%s; default=%s]\n", name, v.Value, v.Source, v.Default); err != nil {
			return err
		}
	}
	return nil
}
