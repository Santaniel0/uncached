// Command uncached runs go test and explains why packages did not come from
// the test cache.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/Santaniel0/uncached/internal/app"
)

const usage = `uncached runs go test and explains why packages were not "(cached)".

Usage:
  uncached run [flags] [--] [go test flags] [packages]
  uncached explain [-state FILE] [-json] [-q]
  uncached diff [-json] OLD_STATE NEW_STATE
  uncached restore-mtime [-state FILE] [-all] [-n]

Run 'uncached <command> -h' for the flags of a command.

Exit status: run exits with go test's status. Any command exits with 2
when uncached itself fails or is used incorrectly.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

const exitError = 2

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitError
	}
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "uncached: %v\n", err)
		return exitError
	}
	tool := &app.Tool{
		GoBin:  "go",
		GitBin: "git",
		Dir:    dir,
		Env:    os.Environ(),
		Stdout: stdout,
		Stderr: stderr,
		Now:    time.Now,
	}
	cmd, rest := args[0], args[1:]
	var code int
	switch cmd {
	case "run":
		code, err = runCmd(ctx, tool, rest)
	case "explain":
		err = explainCmd(ctx, tool, rest)
	case "diff":
		err = diffCmd(tool, rest)
	case "restore-mtime":
		err = restoreCmd(ctx, tool, rest)
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "uncached: unknown command %q\n\n%s", cmd, usage)
		return exitError
	}
	switch {
	case errors.Is(err, flag.ErrHelp):
		return 0
	case err != nil:
		fmt.Fprintf(stderr, "uncached %s: %v\n", cmd, err)
		return exitError
	}
	return code
}

func newFlagSet(name, synopsis string, out io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() {
		fmt.Fprintf(out, "Usage: uncached %s %s\n\nFlags:\n", name, synopsis)
		fs.PrintDefaults()
	}
	return fs
}

func runCmd(ctx context.Context, tool *app.Tool, args []string) (int, error) {
	fs := newFlagSet("run", "[flags] [--] [go test flags] [packages]", tool.Stderr)
	statePath := fs.String("state", "", "state file (default: a file under $GOCACHE/uncached)")
	reportJSON := fs.String("report-json", "", "also write the report as JSON to this file")
	limit := fs.String("content-limit", "4MiB", "hash the content of input files up to this size")
	summary := fs.Bool("q", false, "print only the summary line and notes")
	goBin := fs.String("go", "go", "go command to run")
	if err := fs.Parse(args); err != nil {
		return 0, err
	}
	contentLimit, err := parseSize(*limit)
	if err != nil {
		return 0, fmt.Errorf("-content-limit: %w", err)
	}
	tool.GoBin = *goBin
	return tool.Run(ctx, app.RunOptions{
		StatePath:    *statePath,
		ReportJSON:   *reportJSON,
		ContentLimit: contentLimit,
		Summary:      *summary,
		Args:         fs.Args(),
	})
}

func explainCmd(ctx context.Context, tool *app.Tool, args []string) error {
	fs := newFlagSet("explain", "[flags]", tool.Stderr)
	statePath := fs.String("state", "", "state file (default: a file under $GOCACHE/uncached)")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	summary := fs.Bool("q", false, "print only the summary line and notes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	return tool.Explain(ctx, app.ExplainOptions{StatePath: *statePath, JSON: *asJSON, Summary: *summary})
}

func diffCmd(tool *app.Tool, args []string) error {
	fs := newFlagSet("diff", "[-json] OLD_STATE NEW_STATE", tool.Stderr)
	asJSON := fs.Bool("json", false, "print the report as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		fs.Usage()
		return errors.New("need exactly two state files")
	}
	return tool.Diff(app.DiffOptions{OldPath: fs.Arg(0), NewPath: fs.Arg(1), JSON: *asJSON})
}

func restoreCmd(ctx context.Context, tool *app.Tool, args []string) error {
	fs := newFlagSet("restore-mtime", "[flags]", tool.Stderr)
	statePath := fs.String("state", "", "state file listing the files tests read (default: a file under $GOCACHE/uncached)")
	all := fs.Bool("all", false, "restore every tracked file and directory, not only test inputs")
	dryRun := fs.Bool("n", false, "only report what would be changed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	return tool.RestoreMtime(ctx, app.RestoreOptions{StatePath: *statePath, All: *all, DryRun: *dryRun})
}

// parseSize parses sizes such as "4MiB", "512KiB", "4M" or "1048576".
func parseSize(s string) (int64, error) {
	units := []struct {
		suffix string
		mult   int64
	}{
		{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10},
		{"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10}, {"B", 1},
	}
	mult := int64(1)
	num := strings.TrimSpace(s)
	for _, u := range units {
		if n, ok := strings.CutSuffix(num, u.suffix); ok {
			num, mult = strings.TrimSpace(n), u.mult
			break
		}
	}
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil || n < 0 || n > (1<<62)/mult {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	return n * mult, nil
}
