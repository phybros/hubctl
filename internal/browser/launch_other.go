//go:build !linux

package browser

import (
	"context"
	"fmt"
	"hubctl/internal/config"
	"io"
)

func Launch(context.Context, config.Browser, io.Writer) error {
	return fmt.Errorf("browser launch is supported on Linux; run it in the Pi's GUI terminal")
}
