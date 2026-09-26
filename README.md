# hubctl

A Raspberry Pi home panel controller, written in Go. Development can happen on
macOS; the target is Raspberry Pi OS 64-bit with labwc and Chromium.

## Current scope

The executable loads and validates TOML configuration and supports clean daemon
shutdown on SIGINT and SIGTERM. It serves status over a Unix socket with `0600`
permissions. It does **not** yet control hardware, launch Chromium, or connect to MQTT.

## Development

Requires Go 1.26 or newer.

```sh
go run ./cmd/hubctl config check --config ./config.example.toml
go run ./cmd/hubctl daemon --config ./config.example.toml
# Press Ctrl+C to stop.
# In another terminal while the daemon is running:
go run ./cmd/hubctl status --config ./config.example.toml
go run ./cmd/hubctl status --config ./config.example.toml --json
go run ./cmd/hubctl version
go test ./...
go build -o bin/hubctl ./cmd/hubctl
```

When `--config` is omitted, the file is `hubctl/config.toml` beneath Go's user
configuration directory: `$XDG_CONFIG_HOME` or `~/.config` on Linux, and
`~/Library/Application Support` on macOS. A missing file is an error.

Unknown keys, invalid tab mappings, missing display output, and nonabsolute socket
paths are rejected. Tab mappings default to 1 and 2. The socket defaults to
`$XDG_RUNTIME_DIR/hubctl.sock`; without that environment variable, supply a path.
Paths are literal: environment variables and `~` are not expanded in TOML.
Configuration checking validates values only, not hardware availability or path
permissions. Configuration is read once at startup.

## Raspberry Pi build

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o bin/hubctl-linux-arm64 ./cmd/hubctl
```

## Mode commands

```sh
./bin/hubctl active --config ./config.example.toml
./bin/hubctl screensaver --config ./config.example.toml
./bin/hubctl display-off --config ./config.example.toml
```

These commands now reach the shared controller, but the daemon has no hardware
adapters yet: they return `hardware_unavailable` and exit unsuccessfully. Successful
transitions are exercised with fake adapters in tests; the daemon never simulates
hardware success.

The controller starts with requested/applied modes `unknown` and leaves hardware
untouched. Active and screensaver turn the display on before selecting the browser
tab; display-off leaves the browser alone. Repeated commands reapply their actions.
Commands wait for completion and execute one transition at a time. A transition
has a one-second budget; socket commands have a two-second budget including queue
time. Adapters must honor cancellation. Status stays readable during transitions.

Status includes `requested_mode`, `applied_mode`, `transitioning`, and `last_error`.
Applied mode is unknown during transitions and after failures, including partial
failures. Success means adapter actions completed, not that the physical display
has been independently observed. There is no automatic rollback. A client that
loses its connection may not know the command outcome and should query status.

Status reports daemon version, uptime in seconds, and hardware-control availability.
The client has a three-second timeout. The protocol uses one newline-terminated
JSON request and response per connection, with protocol version `1`:

```json
{"version":1,"command":"status"}
```

The socket's parent directory must already exist; prefer the user's private runtime
directory on the Pi. Clean shutdown removes the socket. Startup refuses to replace
any existing path, including a stale socket left by a crash. If this occurs, verify
that no daemon is running before manually removing that specific stale socket.

Display/browser adapters and hardware validation are next. MQTT, touch, and timers
will feed the same controller in later slices.
