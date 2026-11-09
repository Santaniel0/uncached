// Package app implements the uncached commands on top of the other
// packages. Everything that touches the outside world (directory,
// environment, output streams, clock) comes in through Tool, so the
// commands can be tested end to end against a temporary module.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/Santaniel0/uncached/internal/explain"
	"github.com/Santaniel0/uncached/internal/fingerprint"
	"github.com/Santaniel0/uncached/internal/gocmd"
	"github.com/Santaniel0/uncached/internal/mtime"
	"github.com/Santaniel0/uncached/internal/report"
	"github.com/Santaniel0/uncached/internal/state"
)

// DefaultContentLimit is the largest input file whose content uncached
// hashes to tell an mtime-only change from a content change.
const DefaultContentLimit int64 = 4 << 20

// Tool runs uncached commands.
type Tool struct {
	GoBin  string
	GitBin string
	Dir    string
	Env    []string
	Stdout io.Writer
	Stderr io.Writer
	Now    func() time.Time
}

func (t *Tool) goCmd() gocmd.Command {
	return gocmd.Command{Bin: t.GoBin, Dir: t.Dir, Env: t.Env}
}

func (t *Tool) gitCmd() gocmd.Command {
	return gocmd.Command{Bin: t.GitBin, Dir: t.Dir, Env: t.Env}
}

func (t *Tool) warnf(format string, args ...any) {
	fmt.Fprintf(t.Stderr, "uncached: warning: "+format+"\n", args...)
}

// statePath returns the explicit path, or the default one derived from go
// env.
func (t *Tool) statePath(ctx context.Context, explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	env, err := fingerprint.ReadEnv(ctx, t.goCmd())
	if err != nil {
		return "", err
	}
	return defaultStatePath(env, t.Dir), nil
}

func defaultStatePath(env fingerprint.GoEnv, dir string) string {
	return state.DefaultPath(env.GoCache, env.GoMod, dir)
}

// ExplainOptions configure the explain command.
type ExplainOptions struct {
	StatePath string
	JSON      bool
	Summary   bool
}

// Explain prints the report of the last run again.
func (t *Tool) Explain(ctx context.Context, opts ExplainOptions) error {
	path, err := t.statePath(ctx, opts.StatePath)
	if err != nil {
		return err
	}
	reportPath := state.ReportPath(path)
	data, err := os.ReadFile(reportPath)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("no report at %s; run `uncached run` first: %w", reportPath, err)
	}
	if err != nil {
		return fmt.Errorf("read report: %w", err)
	}
	var rep explain.Report
	if err := json.Unmarshal(data, &rep); err != nil {
		return fmt.Errorf("decode report %s: %w", reportPath, err)
	}
	if opts.JSON {
		return report.JSON(t.Stdout, rep)
	}
	return report.Text(t.Stdout, rep, report.Options{Summary: opts.Summary})
}

// DiffOptions configure the diff command.
type DiffOptions struct {
	OldPath string
	NewPath string
	JSON    bool
}

// Diff compares two saved states.
func (t *Tool) Diff(opts DiffOptions) error {
	old, err := state.Load(opts.OldPath)
	if err != nil {
		return err
	}
	cur, err := state.Load(opts.NewPath)
	if err != nil {
		return err
	}
	rep := explain.FromStates(old, cur, t.Now())
	if opts.JSON {
		return report.JSON(t.Stdout, rep)
	}
	return report.Text(t.Stdout, rep, report.Options{})
}

// RestoreOptions configure the restore-mtime command.
type RestoreOptions struct {
	StatePath string
	All       bool
	DryRun    bool
}

// RestoreMtime sets the mtimes of the files tests read (or of all tracked
// files) to their last commit time.
func (t *Tool) RestoreMtime(ctx context.Context, opts RestoreOptions) error {
	var paths []string
	if !opts.All {
		path, err := t.statePath(ctx, opts.StatePath)
		if err != nil {
			return err
		}
		st, err := state.Load(path)
		if errors.Is(err, state.ErrNoState) {
			return fmt.Errorf("%w; run `uncached run` once so it learns which files the tests read, or pass -all", err)
		}
		if err != nil {
			return err
		}
		for _, in := range st.Inputs() {
			if in.IsFile() {
				paths = append(paths, in.Name)
			}
		}
		if len(paths) == 0 {
			fmt.Fprintln(t.Stdout, "uncached: no recorded test reads files; nothing to restore")
			return nil
		}
	}
	start := t.Now()
	res, err := mtime.Restore(ctx, t.gitCmd(), mtime.Options{Paths: paths, All: opts.All, DryRun: opts.DryRun})
	if err != nil {
		return err
	}
	verb := "restored"
	if opts.DryRun {
		verb = "would restore"
	}
	fmt.Fprintf(t.Stdout, "uncached: %s mtimes of %d files and %d directories from %d commits in %s\n",
		verb, res.Files, res.Dirs, res.Commits, t.Now().Sub(start).Round(time.Millisecond))
	if n := len(res.Skipped); n > 0 {
		shown := res.Skipped[:min(n, 10)]
		fmt.Fprintf(t.Stdout, "uncached: %d inputs are not tracked by git and keep their mtime: %s", n, strings.Join(shown, ", "))
		if n > len(shown) {
			fmt.Fprintf(t.Stdout, ", ...")
		}
		fmt.Fprintln(t.Stdout)
	}
	if res.Shallow {
		t.warnf("this is a shallow clone. Files last changed before the clone boundary get the boundary\n" +
			"commit's time, which moves with every new commit. Fetch the full history\n" +
			"(actions/checkout: fetch-depth: 0) for mtimes that stay stable between commits.")
	}
	return nil
}
