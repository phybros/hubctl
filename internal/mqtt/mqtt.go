// Package mqtt bridges commands and applied state to an optional MQTT broker.
package mqtt

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"hubctl/internal/config"
	"hubctl/internal/controller"
	"hubctl/internal/settings"
)

type Status struct {
	Connected        bool   `json:"connected"`
	LastError        string `json:"last_error,omitempty"`
	LastCommandError string `json:"last_command_error,omitempty"`
}
type Report struct {
	Mode       controller.Mode
	Diagnostic any
}
type queued struct {
	ctx         context.Context
	mode        controller.Mode
	name, value string
	reset       bool
}
type Service struct {
	cfg      config.MQTT
	version  string
	apply    func(context.Context, controller.Mode) error
	report   func() Report
	logger   *slog.Logger
	mu       sync.Mutex
	status   Status
	settings *settings.Store
}

// SetSettings is called before Run; the store is shared with local control.
func (s *Service) SetSettings(store *settings.Store) { s.settings = store }

func New(cfg config.MQTT, version string, apply func(context.Context, controller.Mode) error, report func() Report, logger *slog.Logger) *Service {
	return &Service{cfg: cfg, version: version, apply: apply, report: report, logger: logger}
}
func (s *Service) Snapshot() Status { s.mu.Lock(); defer s.mu.Unlock(); return s.status }
func (s *Service) set(connected bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.Connected = connected
	s.status.LastError = ""
	if err != nil {
		s.status.LastError = err.Error()
	}
}

func (s *Service) Run(ctx context.Context) {
	commands := make(chan queued, 16)
	var worker sync.WaitGroup
	worker.Add(1)
	go func() {
		defer worker.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case command := <-commands:
				if command.ctx.Err() != nil {
					continue
				}
				actionCtx, cancel := context.WithTimeout(command.ctx, 2*time.Second)
				var err error
				if command.name != "" {
					err = s.settings.Change(actionCtx, command.name, command.value, command.reset)
				} else {
					err = s.apply(actionCtx, command.mode)
				}
				cancel()
				s.mu.Lock()
				s.status.LastCommandError = ""
				if err != nil {
					s.status.LastCommandError = err.Error()
				}
				s.mu.Unlock()
				if err != nil {
					s.logger.Warn("MQTT command failed", "mode", command.mode, "setting", command.name, "error", err)
				}
			}
		}
	}()
	defer worker.Wait()
	for ctx.Err() == nil {
		err := s.session(ctx, commands)
		s.set(false, err)
		if ctx.Err() != nil {
			return
		}
		s.logger.Warn("MQTT disconnected; retrying in 5 seconds", "error", err)
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *Service) options(lost chan<- struct{}) *paho.ClientOptions {
	opts := paho.NewClientOptions().AddBroker(s.cfg.Broker).SetClientID("hubctl-" + s.cfg.DeviceID)
	opts.SetUsername(os.Getenv("HUB_MQTT_USERNAME")).SetPassword(os.Getenv("HUB_MQTT_PASSWORD"))
	opts.SetCleanSession(true).SetAutoReconnect(false).SetConnectRetry(false)
	opts.SetConnectTimeout(3 * time.Second).SetWriteTimeout(3 * time.Second).SetKeepAlive(15 * time.Second).SetPingTimeout(3 * time.Second)
	opts.SetWill(s.cfg.TopicPrefix+"/availability", "offline", 1, true)
	if strings.HasPrefix(s.cfg.Broker, "ssl://") {
		opts.SetTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12})
	}
	opts.SetConnectionLostHandler(func(_ paho.Client, _ error) {
		select {
		case lost <- struct{}{}:
		default:
		}
	})
	return opts
}

func wait(ctx context.Context, token paho.Token) error {
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return fmt.Errorf("MQTT operation timed out")
	case <-token.Done():
		return token.Error()
	}
}

func (s *Service) session(ctx context.Context, commands chan<- queued) error {
	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	lost := make(chan struct{}, 1)
	refresh := make(chan struct{}, 1)
	opts := s.options(lost)
	transportCtx, closeTransport := context.WithCancel(context.Background())
	var connMu sync.Mutex
	var transport net.Conn
	opts.SetCustomOpenConnectionFn(func(uri *url.URL, options paho.ClientOptions) (net.Conn, error) {
		dialer := &net.Dialer{Timeout: 3 * time.Second}
		address := uri.Host
		if uri.Port() == "" {
			port := "1883"
			if uri.Scheme == "ssl" {
				port = "8883"
			}
			address = net.JoinHostPort(uri.Hostname(), port)
		}
		var conn net.Conn
		var err error
		if uri.Scheme == "ssl" {
			conn, err = (&tls.Dialer{NetDialer: dialer, Config: options.TLSConfig}).DialContext(ctx, "tcp", address)
		} else {
			conn, err = dialer.DialContext(ctx, "tcp", address)
		}
		if err == nil {
			connMu.Lock()
			if transportCtx.Err() != nil {
				conn.Close()
			} else {
				transport = conn
			}
			connMu.Unlock()
		}
		return conn, err
	})
	client := paho.NewClient(opts)
	defer func() {
		cancel() // Discard any queued commands belonging to this connection.
		clean := false
		if client.IsConnectionOpen() {
			token := client.Publish(s.cfg.TopicPrefix+"/availability", 1, true, "offline")
			clean = token.WaitTimeout(time.Second) && token.Error() == nil
		}
		if clean {
			client.Disconnect(100)
		}
		// Closing the transport also permits the will to run if offline wasn't
		// acknowledged; a clean DISCONNECT alone could leave stale online state.
		closeTransport()
		connMu.Lock()
		if transport != nil {
			transport.Close()
		}
		connMu.Unlock()
		if !clean {
			client.Disconnect(100)
		}
	}()
	token := client.Connect()
	if err := wait(ctx, token); err != nil {
		return fmt.Errorf("MQTT connection failed: %w", err)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	handler := func(_ paho.Client, msg paho.Message) {
		if msg.Topic() == s.cfg.BirthTopic {
			if string(msg.Payload()) == "online" {
				select {
				case refresh <- struct{}{}:
				default:
				}
			}
			return
		}
		if msg.Retained() {
			s.logger.Warn("ignored retained MQTT command")
			return
		}
		command := queued{ctx: sessionCtx}
		if s.settings != nil && strings.HasPrefix(msg.Topic(), s.cfg.TopicPrefix+"/settings/") {
			parts := strings.Split(strings.TrimPrefix(msg.Topic(), s.cfg.TopicPrefix+"/settings/"), "/")
			if len(parts) != 2 {
				return
			}
			command.name, command.value, command.reset = parts[0], string(msg.Payload()), parts[1] == "reset"
			if command.reset {
				if command.value != "RESET" {
					return
				}
			} else if parts[1] != "set" {
				return
			} else if command.name == "day_idle" || command.name == "night_idle" {
				// Number entities use seconds; CLI settings use Go durations.
				seconds, err := strconv.ParseFloat(command.value, 64)
				if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
					command.value = "invalid duration"
				} else {
					command.value = strconv.FormatFloat(seconds, 'f', -1, 64) + "s"
				}
			}
		} else {
			command.mode = controller.Mode(string(msg.Payload()))
			if !command.mode.Valid() {
				s.logger.Warn("ignored invalid MQTT mode command")
				return
			}
		}
		select {
		case commands <- command:
		default:
			s.logger.Warn("MQTT command queue full; command discarded")
		}
	}
	topics := map[string]byte{s.cfg.TopicPrefix + "/mode/set": 1, s.cfg.BirthTopic: 1}
	if s.settings != nil {
		for _, name := range settings.Names {
			topics[s.cfg.TopicPrefix+"/settings/"+name+"/set"] = 1
			topics[s.cfg.TopicPrefix+"/settings/"+name+"/reset"] = 1
		}
		topics[s.cfg.TopicPrefix+"/settings/all/reset"] = 1
	}
	subscription := client.SubscribeMultiple(topics, handler)
	if err := wait(ctx, subscription); err != nil {
		return fmt.Errorf("MQTT subscribe: %w", err)
	}
	for topic, qos := range subscription.(*paho.SubscribeToken).Result() {
		if qos == 0x80 {
			return fmt.Errorf("MQTT broker denied subscription to %s", topic)
		}
	}
	publish := func(topic string, payload any) error { return wait(ctx, client.Publish(topic, 1, true, payload)) }
	syncAll := func() error {
		if err := publish(s.cfg.DiscoveryPrefix+"/select/"+s.cfg.DeviceID+"/mode/config", s.discovery()); err != nil {
			return err
		}
		if s.settings != nil {
			for _, name := range settings.Names {
				component, data := s.settingDiscovery(name)
				if err := publish(s.cfg.DiscoveryPrefix+"/"+component+"/"+s.cfg.DeviceID+"/"+name+"/config", data); err != nil {
					return err
				}
			}
		}
		if err := s.publishState(publish); err != nil {
			return err
		}
		return publish(s.cfg.TopicPrefix+"/availability", "online")
	}
	if err := syncAll(); err != nil {
		return err
	}
	s.set(true, nil)
	s.logger.Info("MQTT connected")
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-lost:
			return fmt.Errorf("MQTT connection lost")
		case <-refresh:
			if err := syncAll(); err != nil {
				return err
			}
		case <-ticker.C:
			if err := s.publishState(publish); err != nil {
				return err
			}
		}
	}
}

func (s *Service) publishState(publish func(string, any) error) error {
	if s.settings != nil {
		for name, v := range s.settings.Snapshot().Values {
			value := v.Value
			if name == "day_idle" || name == "night_idle" {
				d, _ := time.ParseDuration(value)
				value = strconv.FormatFloat(d.Seconds(), 'f', -1, 64)
			}
			if err := publish(s.cfg.TopicPrefix+"/settings/"+name+"/state", value); err != nil {
				return err
			}
		}
	}
	report := s.report()
	mode := string(report.Mode)
	// HA Select uses the special None payload to represent unknown applied state.
	if !report.Mode.Valid() {
		mode = "None"
	}
	if err := publish(s.cfg.TopicPrefix+"/mode/state", mode); err != nil {
		return err
	}
	data, err := json.Marshal(report.Diagnostic)
	if err != nil {
		return err
	}
	return publish(s.cfg.TopicPrefix+"/status", data)
}

func (s *Service) settingDiscovery(name string) (string, []byte) {
	labels := map[string]string{"day_idle": "Day idle timeout", "night_idle": "Night idle timeout", "night_start": "Night start", "night_end": "Night end"}
	component := "text"
	doc := map[string]any{
		"name": labels[name], "unique_id": s.cfg.DeviceID + "_" + name, "entity_category": "config",
		"command_topic": s.cfg.TopicPrefix + "/settings/" + name + "/set", "state_topic": s.cfg.TopicPrefix + "/settings/" + name + "/state",
		"availability_topic": s.cfg.TopicPrefix + "/availability", "qos": 1, "retain": false, "optimistic": false,
		"device": map[string]any{"identifiers": []string{"hubctl_" + s.cfg.DeviceID}},
	}
	if name == "day_idle" || name == "night_idle" {
		component = "number"
		d, _ := time.ParseDuration(s.settings.Snapshot().Values[name].Value)
		doc["min"], doc["max"], doc["step"] = 0.001, max(86400, d.Seconds()), 0.001
		doc["mode"], doc["unit_of_measurement"] = "box", "s"
	} else {
		doc["min"], doc["max"] = 5, 5
		doc["pattern"] = "^([01][0-9]|2[0-3]):[0-5][0-9]$"
	}
	data, _ := json.Marshal(doc)
	return component, data
}

func (s *Service) discovery() []byte {
	data, _ := json.Marshal(map[string]any{
		"name": "Mode", "unique_id": s.cfg.DeviceID + "_mode",
		"command_topic": s.cfg.TopicPrefix + "/mode/set", "state_topic": s.cfg.TopicPrefix + "/mode/state",
		"availability_topic": s.cfg.TopicPrefix + "/availability", "payload_available": "online", "payload_not_available": "offline",
		"options": []string{"active", "screensaver", "display-off"}, "qos": 1, "retain": false, "optimistic": false,
		"device": map[string]any{"identifiers": []string{"hubctl_" + s.cfg.DeviceID}, "name": s.cfg.DeviceName, "manufacturer": "hubctl", "model": "Home panel", "sw_version": s.version},
	})
	return data
}
