// Package gocmd runs external commands (go, git) with an explicit directory
// and environment.
package gocmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Command is a program to run in a fixed directory and environment.
type Command struct {
	Bin string
	Dir string
	Env []string
}

// Output runs the command with args and returns its standard output. On
// failure the error carries the tail of standard error, which is where both
// go and git explain what went wrong.
func (c Command) Output(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	cmd.Dir = c.Dir
	cmd.Env = c.Env
	cmd.WaitDelay = 5 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 2000 {
			msg = "..." + msg[len(msg)-2000:]
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && msg != "" {
			return out, fmt.Errorf("%s %s: %w: %s", c.Bin, strings.Join(args, " "), err, msg)
		}
		return out, fmt.Errorf("%s %s: %w", c.Bin, strings.Join(args, " "), err)
	}
	return out, nil
}
