// Package gocmd runs external commands (go, git) with an explicit directory
// and environment.
package gocmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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

func (c Command) command(ctx context.Context, args []string, stderr *bytes.Buffer) *exec.Cmd {
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	cmd.Dir = c.Dir
	cmd.Env = c.Env
	cmd.WaitDelay = 5 * time.Second
	cmd.Stderr = stderr
	return cmd
}

// Output runs the command with args and returns its standard output. On
// failure the error carries the tail of standard error, which is where both
// go and git explain what went wrong.
func (c Command) Output(ctx context.Context, args ...string) ([]byte, error) {
	var stderr bytes.Buffer
	out, err := c.command(ctx, args, &stderr).Output()
	if err != nil {
		return out, c.wrap(args, err, &stderr)
	}
	return out, nil
}

// Stream runs the command and hands its standard output to consume. If
// consume returns without reading to the end, the command is stopped and
// that is not treated as a failure: callers use it to stop git log early.
func (c Command) Stream(ctx context.Context, args []string, consume func(io.Reader) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var stderr bytes.Buffer
	cmd := c.command(ctx, args, &stderr)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return c.wrap(args, err, &stderr)
	}
	if err := cmd.Start(); err != nil {
		return c.wrap(args, err, &stderr)
	}
	r := &eofReader{r: pipe}
	if err := consume(r); err != nil {
		cancel()
		_ = cmd.Wait()
		return err
	}
	if !r.eof {
		cancel()
		_ = cmd.Wait()
		return nil
	}
	if err := cmd.Wait(); err != nil {
		return c.wrap(args, err, &stderr)
	}
	return nil
}

func (c Command) wrap(args []string, err error, stderr *bytes.Buffer) error {
	msg := strings.TrimSpace(stderr.String())
	if len(msg) > 2000 {
		msg = "..." + msg[len(msg)-2000:]
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && msg != "" {
		return fmt.Errorf("%s %s: %w: %s", c.Bin, strings.Join(args, " "), err, msg)
	}
	return fmt.Errorf("%s %s: %w", c.Bin, strings.Join(args, " "), err)
}

// eofReader remembers whether the reader was drained.
type eofReader struct {
	r   io.Reader
	eof bool
}

func (e *eofReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if errors.Is(err, io.EOF) {
		e.eof = true
	}
	return n, err
}
