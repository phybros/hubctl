package mqtt

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eclipse/paho.mqtt.golang/packets"
	"hubctl/internal/config"
	"hubctl/internal/controller"
	"hubctl/internal/settings"
)

// A small MQTT 3.1.1 wire fixture exercises the real Paho connection, subscription,
// publish acknowledgements, reconnect and shutdown paths without an external broker.
type peer struct {
	conn net.Conn
	mu   sync.Mutex
}

func (p *peer) send(packet packets.ControlPacket) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return packet.Write(p.conn)
}
func (p *peer) publish(topic, payload string, retained bool) error {
	packet := packets.NewControlPacket(packets.Publish).(*packets.PublishPacket)
	packet.TopicName = topic
	packet.Payload = []byte(payload)
	packet.Retain = retained
	return p.send(packet)
}

type connection struct {
	peer   *peer
	packet *packets.ConnectPacket
}
type publication struct {
	topic, payload string
	retained       bool
}
type broker struct {
	listener     net.Listener
	connects     chan connection
	publications chan publication
	wg           sync.WaitGroup
	mu           sync.Mutex
	peers        []*peer
}

func newBroker(t *testing.T) *broker {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := &broker{listener: l, connects: make(chan connection, 8), publications: make(chan publication, 256)}
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			p := &peer{conn: conn}
			b.mu.Lock()
			b.peers = append(b.peers, p)
			b.mu.Unlock()
			b.wg.Add(1)
			go b.serve(p)
		}
	}()
	t.Cleanup(func() {
		l.Close()
		b.mu.Lock()
		for _, p := range b.peers {
			p.conn.Close()
		}
		b.mu.Unlock()
		b.wg.Wait()
	})
	return b
}
func (b *broker) serve(p *peer) {
	defer b.wg.Done()
	defer p.conn.Close()
	for {
		packet, err := packets.ReadPacket(p.conn)
		if err != nil {
			return
		}
		switch packet := packet.(type) {
		case *packets.ConnectPacket:
			b.connects <- connection{p, packet}
			if err := p.send(packets.NewControlPacket(packets.Connack)); err != nil {
				return
			}
		case *packets.SubscribePacket:
			ack := packets.NewControlPacket(packets.Suback).(*packets.SubackPacket)
			ack.MessageID = packet.MessageID
			ack.ReturnCodes = make([]byte, len(packet.Topics))
			for i := range ack.ReturnCodes {
				ack.ReturnCodes[i] = 1
			}
			if err := p.send(ack); err != nil {
				return
			}
		case *packets.PublishPacket:
			b.publications <- publication{packet.TopicName, string(packet.Payload), packet.Retain}
			if packet.Qos == 1 {
				ack := packets.NewControlPacket(packets.Puback).(*packets.PubackPacket)
				ack.MessageID = packet.MessageID
				if err := p.send(ack); err != nil {
					return
				}
			}
		case *packets.PingreqPacket:
			if err := p.send(packets.NewControlPacket(packets.Pingresp)); err != nil {
				return
			}
		case *packets.DisconnectPacket:
			return
		}
	}
}
func await[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for MQTT event")
		var zero T
		return zero
	}
}
func (b *broker) expect(t *testing.T, topic, payload string) publication {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case p := <-b.publications:
			if p.topic == topic && (payload == "" || p.payload == payload) {
				return p
			}
		case <-timer.C:
			t.Fatalf("missing publish %s = %s", topic, payload)
			return publication{}
		}
	}
}

func TestWireLifecycle(t *testing.T) {
	t.Setenv("HUB_MQTT_USERNAME", "panel-user")
	t.Setenv("HUB_MQTT_PASSWORD", "test-password")
	b := newBroker(t)
	cfg := config.DefaultMQTT()
	cfg.Broker = "tcp://" + b.listener.Addr().String()
	var mode atomic.Value
	mode.Store(controller.DisplayOff)
	commands := make(chan controller.Mode, 16)
	s := New(cfg, "test", func(ctx context.Context, m controller.Mode) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		mode.Store(m)
		commands <- m
		return nil
	}, func() Report {
		m := mode.Load().(controller.Mode)
		return Report{Mode: m, Diagnostic: map[string]any{"applied_mode": m}}
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx) }()
	t.Cleanup(func() { cancel(); await(t, done) })
	c := await(t, b.connects)
	if c.packet.ClientIdentifier != "hubctl-kitchen_hub" || c.packet.Username != "panel-user" || string(c.packet.Password) != "test-password" || !c.packet.CleanSession || !c.packet.WillRetain || c.packet.WillTopic != "hub/kitchen/availability" || string(c.packet.WillMessage) != "offline" {
		t.Fatalf("unexpected CONNECT options")
	}
	discovery := b.expect(t, "homeassistant/select/kitchen_hub/mode/config", "")
	if !discovery.retained {
		t.Fatal("discovery not retained")
	}
	b.expect(t, "hub/kitchen/availability", "online")
	if err := c.peer.publish("hub/kitchen/mode/set", "active", true); err != nil {
		t.Fatal(err)
	}
	if err := c.peer.publish("hub/kitchen/mode/set", "reboot", false); err != nil {
		t.Fatal(err)
	}
	if err := c.peer.publish("homeassistant/status", "online", false); err != nil {
		t.Fatal(err)
	}
	b.expect(t, "homeassistant/select/kitchen_hub/mode/config", "")
	b.expect(t, "hub/kitchen/availability", "online")
	select {
	case m := <-commands:
		t.Fatalf("invalid/retained command executed: %s", m)
	default:
	}
	if err := c.peer.publish("hub/kitchen/mode/set", "screensaver", false); err != nil {
		t.Fatal(err)
	}
	if got := await(t, commands); got != controller.Screensaver {
		t.Fatal(got)
	}
	p := b.expect(t, "hub/kitchen/mode/state", "screensaver")
	if !p.retained {
		t.Fatal("state not retained")
	}
	// A local touch/CLI/policy update is also reflected without an MQTT command.
	mode.Store(controller.Active)
	b.expect(t, "hub/kitchen/mode/state", "active")
	mode.Store(controller.Unknown)
	b.expect(t, "hub/kitchen/mode/state", "None")
	c.peer.conn.Close()
	await(t, b.connects)
	b.expect(t, "homeassistant/select/kitchen_hub/mode/config", "")
	b.expect(t, "hub/kitchen/availability", "online")
	cancel()
	b.expect(t, "hub/kitchen/availability", "offline")
	await(t, done)
}

func TestDiscovery(t *testing.T) {
	s := New(config.DefaultMQTT(), "test", nil, nil, nil)
	var d map[string]any
	if err := json.Unmarshal(s.discovery(), &d); err != nil {
		t.Fatal(err)
	}
	if d["retain"] != false || d["optimistic"] != false || d["command_topic"] != "hub/kitchen/mode/set" || d["unique_id"] != "kitchen_hub_mode" {
		t.Fatalf("discovery: %v", d)
	}
	options := d["options"].([]any)
	if len(options) != 3 || options[2] != "display-off" {
		t.Fatal(options)
	}
}

func TestSettingsWire(t *testing.T) {
	b := newBroker(t)
	cfg := config.DefaultMQTT()
	cfg.Broker = "tcp://" + b.listener.Addr().String()
	store, err := settings.Open(filepath.Join(t.TempDir(), "settings.json"), config.DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	s := New(cfg, "test", nil, func() Report { return Report{Mode: controller.Active} }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.SetSettings(store)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx) }()
	t.Cleanup(func() { cancel(); await(t, done) })
	c := await(t, b.connects)
	for _, name := range settings.Names {
		component := "text"
		if strings.HasSuffix(name, "idle") {
			component = "number"
		}
		pub := b.expect(t, "homeassistant/"+component+"/kitchen_hub/"+name+"/config", "")
		var doc map[string]any
		if err := json.Unmarshal([]byte(pub.payload), &doc); err != nil {
			t.Fatal(err)
		}
		if !pub.retained || doc["optimistic"] != false || doc["retain"] != false || doc["entity_category"] != "config" {
			t.Fatal(doc)
		}
	}
	b.expect(t, "hub/kitchen/availability", "online")
	if err := c.peer.publish("hub/kitchen/settings/day_idle/set", "600", false); err != nil {
		t.Fatal(err)
	}
	b.expect(t, "hub/kitchen/settings/day_idle/state", "600")
	if store.Policy().DayIdle != "600s" {
		t.Fatal(store.Policy())
	}
	if err := c.peer.publish("hub/kitchen/settings/night_start/set", "23:00", false); err != nil {
		t.Fatal(err)
	}
	b.expect(t, "hub/kitchen/settings/night_start/state", "23:00")
	// Invalid and retained changes must leave effective values untouched.
	if err := c.peer.publish("hub/kitchen/settings/day_idle/set", "0", false); err != nil {
		t.Fatal(err)
	}
	if err := c.peer.publish("hub/kitchen/settings/night_start/set", "07:00", false); err != nil {
		t.Fatal(err)
	}
	if err := c.peer.publish("hub/kitchen/settings/day_idle/set", "900", true); err != nil {
		t.Fatal(err)
	}
	b.expect(t, "hub/kitchen/settings/day_idle/state", "600")
	if s.Snapshot().LastCommandError == "" {
		t.Fatal("invalid setting not reported")
	}
	if store.Policy().NightStart != "23:00" {
		t.Fatal(store.Policy())
	}
	if err := c.peer.publish("hub/kitchen/settings/all/reset", "RESET", false); err != nil {
		t.Fatal(err)
	}
	b.expect(t, "hub/kitchen/settings/day_idle/state", "300")
	if store.Snapshot().Values["day_idle"].Source != "config" {
		t.Fatal(store.Snapshot())
	}
	// Local changes get published, and HA birth republishes controls.
	if err := store.Change(ctx, "night_end", "08:00", false); err != nil {
		t.Fatal(err)
	}
	b.expect(t, "hub/kitchen/settings/night_end/state", "08:00")
	if err := c.peer.publish("homeassistant/status", "online", false); err != nil {
		t.Fatal(err)
	}
	b.expect(t, "homeassistant/text/kitchen_hub/night_end/config", "")
}

func TestOfflineBrokerDoesNotHangShutdown(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := l.Addr().String()
	l.Close()
	cfg := config.DefaultMQTT()
	cfg.Broker = "tcp://" + address
	s := New(cfg, "test", nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx) }()
	deadline := time.Now().Add(time.Second)
	for s.Snapshot().LastError == "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if state := s.Snapshot(); state.Connected || !strings.Contains(state.LastError, "connection failed") {
		t.Fatalf("state: %+v", state)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("offline broker blocked shutdown")
	}
}
