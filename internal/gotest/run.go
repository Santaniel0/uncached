package gotest

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/Santaniel0/uncached/internal/envvars"
)

// Invocation describes one go test run.
type Invocation struct {
	GoBin  string
	Dir    string
	Env    []string
	Args   []string
	Stdout io.Writer
	Stderr io.Writer
}

// Run is what a go test invocation produced.
type Run struct {
	ExitCode int
	Results  *Results
	Debug    *DebugLog
}

// Execute runs go test with the test cache debug output switched on. The
// child's standard output is copied to inv.Stdout byte for byte; its
// standard error is copied to inv.Stderr except for the debug lines, which
// are parsed instead. A non-zero exit status is reported in Run.ExitCode, not
// as an error.
func Execute(ctx context.Context, inv Invocation) (*Run, error) {
	cmd := exec.CommandContext(ctx, inv.GoBin, append([]string{"test"}, inv.Args...)...)
	cmd.Dir = inv.Dir
	cmd.Env = WithCacheDebug(inv.Env)
	cmd.WaitDelay = 5 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("go test: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("go test: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start go test: %w", err)
	}

	run := &Run{Results: NewResults(), Debug: NewDebugLog()}
	var wg sync.WaitGroup
	var outErr, errErr error
	wg.Go(func() {
		outErr = pump(stdout, func(chunk string) error {
			run.Results.Line(chunk)
			_, err := io.WriteString(inv.Stdout, chunk)
			return err
		})
	})
	wg.Go(func() {
		errErr = pump(stderr, func(chunk string) error {
			if run.Debug.Line(chunk) {
				return nil
			}
			_, err := io.WriteString(inv.Stderr, chunk)
			return err
		})
	})
	// The pipes must be drained before Wait closes them.
	wg.Wait()
	waitErr := cmd.Wait()

	if ctx.Err() != nil {
		return nil, fmt.Errorf("go test: %w", context.Cause(ctx))
	}
	var exitErr *exec.ExitError
	switch {
	case errors.As(waitErr, &exitErr):
		run.ExitCode = exitErr.ExitCode()
	case waitErr != nil:
		return nil, fmt.Errorf("go test: %w", waitErr)
	}
	if err := errors.Join(outErr, errErr); err != nil {
		return run, fmt.Errorf("copy go test output: %w", err)
	}
	return run, nil
}

// pump reads r line by line, keeping the newline, and hands each line to
// fn. An error from fn does not stop it: the child must never block on a
// full pipe, and the lines still need parsing. The first error is reported
// at the end.
func pump(r io.Reader, fn func(string) error) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var first error
	for {
		chunk, err := br.ReadString('\n')
		if chunk != "" {
			if ferr := fn(chunk); ferr != nil && first == nil {
				first = ferr
			}
		}
		if errors.Is(err, io.EOF) {
			return first
		}
		if err != nil {
			return errors.Join(first, err)
		}
	}
}

// WithCacheDebug returns env with gocachetest=1 added to GODEBUG, keeping
// any other settings in their original order. The result is deterministic,
// which matters: cmd/go hashes GODEBUG into every test's cache key.
func WithCacheDebug(env []string) []string {
	current, _ := envvars.Lookup(env, "GODEBUG")
	var parts []string
	for p := range strings.SplitSeq(current, ",") {
		p = strings.TrimSpace(p)
		if p == "" || strings.HasPrefix(p, "gocachetest=") {
			continue
		}
		parts = append(parts, p)
	}
	parts = append(parts, "gocachetest=1")
	return envvars.Set(env, "GODEBUG", strings.Join(parts, ","))
}
