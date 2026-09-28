# hubctl

A Raspberry Pi home panel controller, written in Go. Development can happen on
macOS; the target is Raspberry Pi OS 64-bit with labwc and Chromium.

## Current scope

The executable loads and validates TOML configuration and supports clean daemon
shutdown on SIGINT and SIGTERM. It serves status over a Unix socket with `0600`
permissions. On Linux, it controls display output with `wlr-randr` and Chromium tab
selection through Chromium's local DevTools HTTP interface. It does not launch
Chromium yet. Optional MQTT control and Home Assistant discovery are available.
`wtype` is no longer required.

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
paths are rejected. Browser defaults are the named `home` and `slideshow` tabs
shown in the example config. The socket defaults to
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

On Linux display commands execute `wlr-randr` directly, without a shell; browser
commands list and activate DevTools targets over local HTTP.
The daemon inherits its Wayland environment; it does not assume `wayland-0`.
On macOS, or when the Linux session environment/tools are missing, commands return
`hardware_unavailable`. Status includes a `hardware_error` explaining why. Restart
the daemon after installing missing tools or correcting its environment.
`hardware_control: true` means the adapters are available, not that hardware has
been verified. Tool failures and bounded command output are included in errors.

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

## Manual Pi test

Copy `bin/hubctl-linux-arm64` to the Pi as `hubctl` and make it executable with
`chmod +x ./hubctl`. No Go installation is needed on the Pi. In a terminal opened
from the Pi's Wayland desktop, install the tools and identify the output:

```sh
sudo apt install wlr-randr
wlr-randr
```

Create `config.toml` with the actual output name (the value below is an example):

```toml
[display]
output = "HDMI-A-1"

[browser]
endpoint = "http://127.0.0.1:9222"
active_tab = "home"
screensaver_tab = "slideshow"

[[browser.tabs]]
name = "home"
url = "http://homeassistant.local"

[[browser.tabs]]
name = "slideshow"
url = "https://www.google.com"
```

Tabs are a configurable array with unique names and URLs. The mode mappings refer
to names, not numeric positions. You can add more named tabs; currently only the
active and screensaver mappings are selectable by mode commands. A supplied tabs
array replaces the defaults. Old numeric `active_tab`/`screensaver_tab` settings
must be replaced with names when upgrading.

Each command discovers current target IDs, so tab reordering and Chromium restarts
do not invalidate saved IDs. Matching requires the same scheme and host (including
an explicit port, if present), and the configured path or a descendant path.
Queries and fragments are ignored. For example, the Home Assistant root URL also
matches `/lovelace/0`. `/photos` matches `/photos/album` but not `/photos-other`.
Use distinct paths for apps on the same host; avoid overlapping configured paths.
If a site redirects to a different host, configure its final URL. Missing or
multiple matching page targets are errors. The daemon does not create, navigate,
or close tabs; open the configured pages yourself.

The endpoint must use an HTTP loopback IP address. Requests do not follow redirects
or use HTTP proxies. Keep Chromium's debugging listener bound to loopback.

Omitting `[socket]` uses the graphical user's `$XDG_RUNTIME_DIR/hubctl.sock`.
Start the daemon in that desktop terminal, as the logged-in user, without sudo:

```sh
./hubctl daemon --config ./config.toml
```

Launch Chromium manually with the configured pages in any order. With the previous
kiosk instance closed, launch from another desktop terminal:

```sh
chromium --kiosk --noerrdialogs --no-first-run \
  --user-data-dir="$HOME/.config/hubctl-chromium" \
  --remote-debugging-address=127.0.0.1 \
  --remote-debugging-port=9222 \
  http://homeassistant.local https://www.google.com
```

The dedicated profile is reused across launches and preserves logins. An already
running browser using that profile must be restarted to change its startup flags.

From SSH as the same user, use the same config file to run:

```sh
./hubctl status --config ./config.toml
./hubctl screensaver --config ./config.toml
./hubctl active --config ./config.toml
./hubctl display-off --config ./config.toml
./hubctl active --config ./config.toml
```

If SSH lacks `XDG_RUNTIME_DIR`, set `[socket].path` in the config to the absolute
socket path logged by the daemon. The CLI does not need Wayland access.
No keyboard focus or shortcuts are needed for tab activation. Keep SSH available
to turn the output back on while validating touch wake. Validate display wake and
tab switching on the real panel.

The command syntax follows the upstream [wlr-randr source](https://github.com/emersion/wlr-randr/blob/master/main.c)
and Chromium's DevTools `/json/list` and `/json/activate/{id}` endpoints.
MQTT commands also feed the shared controller and local policy.

## Touch-to-wake

On Linux arm64/amd64, enable touch monitoring by adding this top-level table
(adjust the device path for your touchscreen):

```toml
[input]
device = "/dev/input/by-id/usb-wch.cn_TouchScreen_9LQ0172005164-event-if00"
wake_delay = "4s"
```

Use the stable `by-id` path, not an `eventN` number. The desktop user must be able
to read the device. Do not run the daemon as root. If configured input cannot be
opened or lacks aggregate `BTN_TOUCH` contact reporting, startup fails explicitly.
Omit `[input]` on macOS or to disable monitoring. Stop `evtest --grab` before testing.

Before entering screensaver or display-off, hubctl exclusively grabs the input
device. Grab failure rejects the transition before changing hardware. A finger
already on the active screen also rejects the transition: lift it and retry.
The first new touch requests active through the shared controller. Input remains
grabbed until activation succeeds and all fingers are lifted, including when
active was requested manually. A failed activation keeps capture; lift and touch
again to retry, or use the CLI. Repeated movement does not keep issuing commands.
While active, touch is passively observed and normal gestures reach Chromium.

`hubctl status` reports whether input is grabbed and a contact is down. JSON status
also includes the last activity time. Timestamp-only frames are not activity.
Kernel event overruns are resynchronized before allowing release. Device read or
release errors appear in input status and block further transitions; correct the
problem and restart the daemon. Automatic hotplug recovery is not implemented.

Capture is owned by the running process. Shutdown/crash releases it, and unplugging
the device removes it; protection does not persist across daemon restarts. Restore
active before stopping the daemon when practical. Other touch interfaces/devices
are not captured: verify your chosen interface blocks all Chromium touch input
using `evtest --grab`, as was verified for this panel.

Manual acceptance test after copying the rebuilt binary and restarting:

1. Set `screensaver`, then tap: Home Assistant should appear without receiving the tap.
2. Set `display-off`, wait for the monitor to power down, then tap to wake.
3. Hold a finger down while waking: status should remain grabbed until it lifts.
4. Try two fingers; releasing only one must not release capture.
5. Verify subsequent taps and scrolling work normally in Home Assistant.

The software verifies successful display/browser commands, not physical monitor
readiness; the monitor can still take additional time to illuminate after wake.
Set `input.wake_delay = "4s"` to keep input captured during that interval. The
countdown begins after a successful transition turns the output back on following
display-off; release requires both expiry and all fingers lifted. Additional
touches and repeated active commands do not restart the countdown. Manual active
commands receive the same protection. Ordinary screensaver-to-active transitions
have no delay; going from off through screensaver still preserves the countdown.
Failed wake attempts retain capture and start the countdown only on success.
Omitting the setting or using `"0s"` preserves immediate release after finger-up.
Commands return without waiting for the delay. Status reports its remaining time.

## Idle and day/night policy

With touch input configured, enable local automation using this top-level table:

```toml
[policy]
enabled = true
day_idle = "5m"
night_idle = "2m"
night_start = "22:00"
night_end = "07:00"
timezone = "Local"
```

Automation is disabled when omitted. The durations and hours shown are the defaults.
`Local` uses the Pi's timezone; set an IANA name such as `America/Toronto` to make it
explicit. Hours use 24-hour HH:MM format. Night includes its start and excludes its
end; intervals may cross midnight. Equal start/end times and nonpositive idle
durations are rejected. The schedule is evaluated locally and requires no HA/MQTT.

When enabled, startup selects active during the day or display-off at night.
Daytime active becomes screensaver after the day idle timeout. Night start selects
display-off; touch wakes to active, and night idle returns to display-off. A manually
selected screensaver at night also expires to display-off. Morning selects active.
Nighttime wake uses the same active mode, not a separate CLI mode.

Manual commands take effect immediately and reset the idle timer, including
repeated commands. They supersede pending automatic retries; they are not permanent
overrides of the schedule. Manually selecting display-off during the day stays off
until touch/manual wake or a morning boundary. Touch-down, motion, and finger-up
count as activity. Held fingers and the configured wake delay prevent idle expiry.
Timestamp-only reports do not reset the timer. Mouse and keyboard activity are not
monitored. A night boundary encountered during a held gesture waits for finger-up.

The policy checks once per second and uses the existing serialized controller and
touch capture. Failed automatic transitions retry at five-second intervals; new
activity cancels stale idle retries. A missing/failed touch monitor suspends
automatic transitions and is reported in status. Normal wake/input error recovery
still applies. `hubctl status --json` includes policy period, timezone, idle seconds,
pending mode, and errors. Status remains available during hardware actions.

To test without waiting five minutes, temporarily set `day_idle = "15s"` and
`night_idle = "10s"`, restart the daemon, and verify touch resets the timer. Choose
night hours surrounding the current local time to test nighttime startup and wake,
then place an upcoming boundary a minute ahead to check scheduled transitions.
Restore the intended timings and restart afterward.

## MQTT and Home Assistant

MQTT is disabled by default. Add a top-level section using your actual broker:

```toml
[mqtt]
enabled = true
broker = "tcp://mqtt.local:1883"
device_id = "kitchen_hub"
device_name = "Kitchen Hub"
topic_prefix = "hub/kitchen"
discovery_prefix = "homeassistant"
birth_topic = "homeassistant/status"
```

Use unique, stable device IDs and topic prefixes for multiple panels. The client ID
is `hubctl-<device_id>`. `ssl://broker:8883` enables TLS with certificate verification
against system trust roots; plain `tcp://` does not encrypt credentials or traffic.
Custom CA files and client certificates are not configured in this version.

Credentials come exclusively from `HUB_MQTT_USERNAME` and `HUB_MQTT_PASSWORD` in the
daemon's environment. For manual startup, optionally keep these exports in
`~/mqtt.env`, protect it with `chmod 600 ~/mqtt.env`, then source it in the GUI
terminal before starting the daemon:

```sh
# Contents of ~/mqtt.env (replace the placeholders):
export HUB_MQTT_USERNAME='panel-user'
export HUB_MQTT_PASSWORD='your-broker-password'
```

```sh
. ~/mqtt.env
~/hubctl daemon --config ~/config.toml
```

Do not put credentials in the broker URL or TOML. Leave the environment variables
unset for a broker that allows unauthenticated access.

With the example prefix, the bridge uses:

| Topic | Behavior |
| --- | --- |
| `hub/kitchen/mode/set` | Accepts `active`, `screensaver`, or `display-off` |
| `hub/kitchen/mode/state` | Retained applied mode; `None` means unknown to HA |
| `hub/kitchen/status` | Retained diagnostic JSON, including controller/input/policy status |
| `hub/kitchen/availability` | Retained `online`/`offline`, with an offline Last Will |
| `homeassistant/select/kitchen_hub/mode/config` | Retained Home Assistant discovery |

Home Assistant's MQTT integration must already be configured for the same broker.
The Kitchen Hub device will expose a Mode selector. It is not optimistic: a failed
transition is not reported as successful. Discovery and state are republished on
reconnection and on `online` received at the configured HA birth topic. The select
uses [Home Assistant's MQTT Select schema](https://www.home-assistant.io/integrations/select.mqtt/).

State and diagnostics refresh once per second, including changes caused by touch,
CLI commands, and local scheduling. MQTT mode commands behave like CLI commands:
they reset idle timing and do not permanently override the day/night schedule.
Send commands without retain; retained commands replayed on subscription are
ignored. MQTT 3.1.1 may deliver live retained publications without marking them as
retained to existing subscribers, so publishing retained commands is unsupported.
QoS 1 can redeliver messages; repeated mode commands reapply and reset idle time.

Connections retry every five seconds. MQTT runs independently of local control:
broker outages do not suspend touch, schedules, or the CLI. The command queue is
bounded, and queued commands from a lost connection are discarded. Connection
state/errors appear in `hubctl status`; command failures appear in the daemon log
and controller diagnostics. On shutdown, the bridge attempts to publish offline;
on unexpected disconnection the broker publishes the Last Will after detecting it.

For manual verification, use Home Assistant's MQTT publish/listen tools to send
`screensaver` to `hub/kitchen/mode/set` (retain off), watch state change, then use
touch to wake and verify state returns to active. Test broker restart and HA restart;
the mode selector and current state should recover without restarting hubctl.

## Runtime settings

Four policy preferences can be changed without restarting the daemon:
`day_idle`, `night_idle`, `night_start`, and `night_end`. Home Assistant discovers
two MQTT Number controls (idle timeouts in seconds) and two MQTT Text controls
(night start/end in strict 24-hour `HH:MM` format), under the device's configuration
entities. These use the [MQTT Number](https://www.home-assistant.io/integrations/number.mqtt/)
and [MQTT Text](https://www.home-assistant.io/integrations/text.mqtt/) schemas.
The duration boxes normally cover 0.001–86400 seconds; discovery expands the upper
bound if the effective value is already larger. CLI/TOML accept any positive Go
duration. Night start and end must differ. Both times use `policy.timezone`.

The same settings are available locally:

```sh
~/hubctl settings list --config ~/config.toml
~/hubctl settings set day_idle 10m --config ~/config.toml
~/hubctl settings set night_start 23:00 --config ~/config.toml
~/hubctl settings reset day_idle --config ~/config.toml
~/hubctl settings reset all --config ~/config.toml
```

`settings list --json` and `status --json` expose each setting's effective value,
TOML default, and source (`config` or `override`). Human-readable status shows
these too. Flags go after the setting name/value. Settings commands require the
daemon to be running.

TOML remains the installation configuration and supplies defaults. Edits made
through MQTT or the CLI are saved as overrides in
`$XDG_STATE_HOME/hubctl/settings.json`, falling back to
`~/.local/state/hubctl/settings.json`. An optional `[settings] path` selects a
different absolute file path. Use a separate file for each daemon instance.
The file is created on the first change, with mode 0600; updates sync a temporary
file and atomically rename it. TOML is never rewritten. Invalid edits and failed
writes leave both the existing overrides and live policy unchanged.

Overrides survive restarts and broker outages. Editing a TOML default does not
replace an override: reset that setting to return to the default loaded at daemon
startup. Restart to reload TOML edits. Resetting `all` removes overrides together;
this is useful if an individual schedule reset would make start and end equal.
A malformed or incompatible override file stops startup with an error instead of
silently discarding preferences. Back up/move it aside while the daemon is stopped
to recover with TOML defaults. `config check` checks TOML only, not runtime state.

Behavior on accepted changes:

- Idle timeout changes preserve elapsed idle time. Shortening a timeout can cause
  an immediate idle transition; lengthening it cancels a pending idle retry.
- Schedule changes re-evaluate the current period immediately. Crossing into
  night requests display-off; crossing into day requests active. Touch safety
  still applies. Changes that stay in the same period do not reset idle time.
- Hardware transition failures do not undo a saved setting: policy status records
  the error and the existing retry mechanism handles the transition.
- When `policy.enabled = false`, settings can still be saved, but no automation
  runs. Enable policy in TOML and restart when ready.
- Wake delay, timezone, policy enablement, browser configuration, broker settings,
  and hardware settings remain TOML-only in this release.

With the example topic prefix:

| Topic | Payload |
| --- | --- |
| `hub/kitchen/settings/day_idle/set` | Numeric seconds, e.g. `600` |
| `hub/kitchen/settings/night_idle/set` | Numeric seconds, e.g. `120` |
| `hub/kitchen/settings/night_start/set` | `HH:MM`, e.g. `22:00` |
| `hub/kitchen/settings/night_end/set` | `HH:MM`, e.g. `07:00` |
| `hub/kitchen/settings/NAME/reset` | `RESET`; NAME is a setting name or `all` |
| `hub/kitchen/settings/NAME/state` | Retained confirmed value (seconds or `HH:MM`) |

All commands must be published without retain. Controls are non-optimistic and
show confirmed settings; rejected commands leave the old state in place. Errors
are logged and the most recent processed MQTT command error is exposed in
`status.mqtt.last_command_error` (cleared by the next successful command).
The retained diagnostic status includes settings values, defaults, and sources.
Duplicate identical settings commands are no-ops. Discovery and current values
are republished on broker reconnect and HA birth, never restored from retained
command messages. Reset is currently available through CLI/MQTT, not an HA button.

To test: change Day idle timeout to `600` in HA, confirm an override with
`settings list`, restart the daemon, and check it remains `600`. Reset `day_idle`
through the CLI and verify HA returns to the TOML default (normally `300`).
