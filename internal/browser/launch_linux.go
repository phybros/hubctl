//go:build linux

package browser

import (
	"context"
	"fmt"
	"io"
	"os"
	"syscall"

	"hubctl/internal/config"
)

// Launch replaces the launcher process with Chromium. Signals, terminal output,
// and exit status belong directly to the browser; the daemon is not involved.
func Launch(ctx context.Context, cfg config.Browser, stderr io.Writer) error {
	if os.Getenv("WAYLAND_DISPLAY") == "" || os.Getenv("XDG_RUNTIME_DIR") == "" {
		return fmt.Errorf("browser launch requires the Wayland desktop session; run it in the Pi's GUI terminal")
	}
	p, err := planLaunch(cfg)
	if err != nil {
		return err
	}
	if err := p.preflight(ctx); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "Launching Chromium with profile %s (password-store=basic; no keyring protection for saved passwords)\n", p.profile)
	if err := ctx.Err(); err != nil {
		return err
	}
	return syscall.Exec(p.executable, append([]string{p.executable}, p.args...), os.Environ())
}
