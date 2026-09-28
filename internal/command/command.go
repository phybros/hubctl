// Package command runs hardware tools directly, without a shell.
package command

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type Runner interface {
	Run(context.Context, string, ...string) error
}
type Exec struct{}

func (Exec) Run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	// Bound waits for inherited output pipes as well as the process itself.
	cmd.WaitDelay = 100 * time.Millisecond
	output := &limitedOutput{}
	cmd.Stdout, cmd.Stderr = output, output
	err := cmd.Run()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("%s: %w; output: %s", name, err, strings.TrimSpace(string(output.data)))
	}
	return nil
}

// Limit diagnostics so a noisy tool cannot consume unbounded memory or overflow
// the control protocol's response frame.
type limitedOutput struct{ data []byte }

func (b *limitedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := 512 - len(b.data); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		b.data = append(b.data, p...)
	}
	return n, nil
}
