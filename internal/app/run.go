package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/Santaniel0/uncached/internal/envvars"
	"github.com/Santaniel0/uncached/internal/explain"
	"github.com/Santaniel0/uncached/internal/fingerprint"
	"github.com/Santaniel0/uncached/internal/gocache"
	"github.com/Santaniel0/uncached/internal/gotest"
	"github.com/Santaniel0/uncached/internal/ids"
	"github.com/Santaniel0/uncached/internal/inputs"
	"github.com/Santaniel0/uncached/internal/report"
	"github.com/Santaniel0/uncached/internal/state"
	"github.com/Santaniel0/uncached/internal/testlog"
)

// RunOptions configure the run command.
type RunOptions struct {
	StatePath    string
	ReportJSON   string
	ContentLimit int64
	Summary      bool
	// Args are passed to go test unchanged.
	Args []string
}

// Run runs go test, explains every package that did not come from the
// cache, and records state for the next run. It returns go test's exit
// code. Problems in the analysis are reported as notes and warnings and
// never change that exit code: a CI job must fail or pass because of its
// tests, not because of uncached.
func (t *Tool) Run(ctx context.Context, opts RunOptions) (int, error) {
	goEnv, err := fingerprint.ReadEnv(ctx, t.goCmd())
	if err != nil {
		return 0, err
	}
	if goEnv.GoCache == "off" {
		t.warnf("GOCACHE=off: go test caches nothing, so there is nothing to explain")
		return t.execute(ctx, opts.Args)
	}
	limit := opts.ContentLimit
	if limit <= 0 {
		limit = DefaultContentLimit
	}
	statePath := opts.StatePath
	if statePath == "" {
		statePath = defaultStatePath(goEnv, t.Dir)
	}

	var notes []explain.Note
	prev, err := state.Load(statePath)
	switch {
	case errors.Is(err, state.ErrNoState):
		prev = nil // FromRun notes the missing state itself
	case err != nil:
		prev = nil
		notes = append(notes, analysisNote("previous state ignored", err))
	}

	// Tests see GODEBUG with gocachetest=1 added, and cmd/go hashes that
	// value, so inputs must be observed in the same environment.
	observer := inputs.Observer{
		LookupEnv:    envvars.LookupFunc(gotest.WithCacheDebug(t.Env)),
		ContentLimit: limit,
	}
	preRun := map[testlog.Input]inputs.Observation{}
	var prevObs []inputs.Observation
	if prev != nil {
		prevObs = prev.Observations()
		observer.Known = inputs.KnownFiles(prevObs)
		// Observe before go test runs: these are the values cmd/go will
		// compare against, and tests may rewrite their own inputs.
		obs, err := observer.Observe(ctx, prev.Inputs())
		if err != nil {
			notes = append(notes, analysisNote("observe inputs before the run", err))
		}
		for _, o := range obs {
			preRun[o.Input] = o
		}
		observer.Known = inputs.KnownFiles(prevObs, obs)
	}

	run, err := gotest.Execute(ctx, gotest.Invocation{
		GoBin: t.GoBin, Dir: t.Dir, Env: t.Env, Args: opts.Args,
		Stdout: t.Stdout, Stderr: t.Stderr,
	})
	if run == nil {
		return 0, err
	}
	if err != nil {
		notes = append(notes, analysisNote("pass through go test output", err))
	}

	a := analysis{
		tool: t, goEnv: goEnv, args: gotest.ParseArgs(opts.Args), run: run, prev: prev,
		observer: observer, preRun: preRun, statePath: statePath, now: t.Now(),
	}
	rep, next := a.analyze(ctx)
	rep.Notes = append(notes, rep.Notes...)

	fmt.Fprintln(t.Stderr)
	if err := report.Text(t.Stderr, rep, report.Options{Summary: opts.Summary}); err != nil {
		t.warnf("%v", err)
	}
	t.saveReport(rep, state.ReportPath(statePath))
	if opts.ReportJSON != "" {
		t.saveReport(rep, opts.ReportJSON)
	}
	if err := state.Save(statePath, next); err != nil {
		t.warnf("state not saved: %v", err)
	}
	return run.ExitCode, nil
}

func (t *Tool) execute(ctx context.Context, args []string) (int, error) {
	run, err := gotest.Execute(ctx, gotest.Invocation{
		GoBin: t.GoBin, Dir: t.Dir, Env: t.Env, Args: args, Stdout: t.Stdout, Stderr: t.Stderr,
	})
	if run == nil {
		return 0, err
	}
	return run.ExitCode, nil
}

func (t *Tool) saveReport(rep explain.Report, path string) {
	var buf bytes.Buffer
	if err := report.JSON(&buf, rep); err != nil {
		t.warnf("%v", err)
		return
	}
	if err := state.WriteFileAtomic(path, buf.Bytes()); err != nil {
		t.warnf("report not saved: %v", err)
	}
}

func analysisNote(what string, err error) explain.Note {
	return explain.Note{Kind: explain.NoteAnalysisFailed, Detail: what + ": " + err.Error()}
}

// analysis turns one go test run into a report and the next state.
type analysis struct {
	tool      *Tool
	goEnv     fingerprint.GoEnv
	args      gotest.Args
	run       *gotest.Run
	prev      *state.State
	observer  inputs.Observer
	preRun    map[testlog.Input]inputs.Observation
	statePath string
	now       time.Time
	notes     []explain.Note
}

func (a *analysis) note(what string, err error) {
	a.notes = append(a.notes, analysisNote(what, err))
}

func (a *analysis) analyze(ctx context.Context) (explain.Report, *state.State) {
	results := a.run.Results.All()
	var tested []ids.ImportPath
	for _, r := range results {
		if r.Outcome.Ran() {
			tested = append(tested, r.Package)
		}
	}

	listed := map[ids.ImportPath]fingerprint.Package{}
	if len(tested) > 0 {
		l, err := fingerprint.List(ctx, a.tool.goCmd(), a.args.ListFlags(), tested)
		if err != nil {
			a.note("go list", err)
		} else {
			listed = l
		}
	}
	fps, files, err := fingerprint.Build(ctx, listed, tested, 0)
	if err != nil {
		a.note("fingerprint test binaries", err)
	}
	cfg := fingerprint.NewConfig(a.goEnv, a.args.Flags)

	current := make(map[ids.ImportPath]*state.TestRecord, len(tested))
	for _, p := range tested {
		rec := &state.TestRecord{Config: cfg, RecordedAt: a.now}
		if pkg, ok := listed[p]; ok {
			rec.Dir, rec.Root = pkg.Dir, pkg.Root
		}
		if fp, ok := fps[p]; ok {
			rec.Binary = fp
		}
		current[p] = rec
	}

	rep := explain.FromRun(explain.Run{
		Results:      results,
		Debug:        a.run.Debug,
		Prev:         a.prev,
		Current:      current,
		CurrentFiles: files,
		PreRun:       a.preRun,
		Now:          a.now,
		StatePath:    a.statePath,
	})
	next := a.nextState(ctx, results, current, files)
	rep.Notes = append(rep.Notes, a.notes...)
	return rep, next
}

// nextState builds the state to save. Records for packages this run did not
// test are carried over unchanged.
func (a *analysis) nextState(ctx context.Context, results []gotest.Result, current map[ids.ImportPath]*state.TestRecord, files fingerprint.Files) *state.State {
	next := state.New()
	next.SavedAt = a.now
	next.Commit = a.commit(ctx)
	if a.prev != nil {
		maps.Copy(next.Tests, a.prev.Tests)
		maps.Copy(next.Files, a.prev.Files)
	}
	maps.Copy(next.Files, files)

	var cache *gocache.Dir
	if a.goEnv.GoCacheProg != "" {
		a.notes = append(a.notes, explain.Note{Kind: explain.NoteRemoteCache})
	} else if c, err := gocache.Open(a.goEnv.GoCache); err != nil {
		a.note("open GOCACHE", err)
	} else {
		cache = c
	}

	var unreadable []string
	for _, r := range results {
		if !r.Outcome.Ran() {
			continue
		}
		rec := current[r.Package]
		var prevRec *state.TestRecord
		if a.prev != nil {
			prevRec = a.prev.Tests[r.Package]
		}
		testIDs, inputID := storedIDs(a.run.Debug.Events(r.Package), r.Outcome)
		if len(testIDs) == 0 {
			// Nothing new went into GOCACHE (the test failed, caching was
			// off, or an input was too new). An earlier record still
			// describes what the cache holds.
			if prevRec == nil {
				rec.NoResult = true
				next.Tests[r.Package] = rec
			}
			continue
		}
		rec.TestIDs, rec.InputID = testIDs, inputID
		if prevRec != nil && prevRec.InputID == inputID && prevRec.HasTestID(testIDs) {
			for _, id := range prevRec.TestIDs {
				if !slices.Contains(rec.TestIDs, id) {
					rec.TestIDs = append(rec.TestIDs, id)
				}
			}
		}
		if cache != nil {
			obs, err := a.readInputs(ctx, cache, testIDs[0], rec)
			if err != nil {
				unreadable = append(unreadable, fmt.Sprintf("%s: %v", r.Package, err))
			} else {
				rec.Inputs = obs
			}
		}
		next.Tests[r.Package] = rec
	}
	if len(unreadable) > 0 {
		a.notes = append(a.notes, explain.Note{
			Kind:   explain.NoteAnalysisFailed,
			Count:  len(unreadable),
			Detail: "inputs not recorded for " + strings.Join(unreadable[:min(len(unreadable), 3)], "; "),
		})
	}
	pruneFiles(next)
	return next
}

// storedIDs returns the test IDs and input ID under which cmd/go stored or
// found this package's result in this run.
func storedIDs(evs []gotest.Event, outcome gotest.Outcome) ([]ids.ActionID, ids.ActionID) {
	var (
		testIDs []ids.ActionID
		inputID ids.ActionID
	)
	for _, e := range evs {
		if e.Kind == gotest.EventSaved && !slices.Contains(testIDs, e.TestID) {
			testIDs = append(testIDs, e.TestID)
			inputID = e.InputID
		}
	}
	if len(testIDs) == 0 && outcome == gotest.OutcomeCached {
		// The last successful input hash is the one that hit.
		for _, e := range slices.Backward(evs) {
			if e.Kind == gotest.EventInputsHashed {
				return []ids.ActionID{e.TestID}, e.InputID
			}
		}
	}
	return testIDs, inputID
}

var errNoPackageDir = errors.New("package directory unknown")

func (a *analysis) readInputs(ctx context.Context, cache *gocache.Dir, testID ids.ActionID, rec *state.TestRecord) ([]inputs.Observation, error) {
	if rec.Dir == "" {
		return nil, errNoPackageDir
	}
	data, _, err := cache.Read(testID)
	if err != nil {
		return nil, err
	}
	entries, err := testlog.Parse(data)
	if err != nil {
		return nil, err
	}
	return a.observer.Observe(ctx, testlog.Resolve(entries, rec.Dir, rec.Root))
}

func (a *analysis) commit(ctx context.Context) string {
	if a.tool.GitBin == "" {
		return ""
	}
	out, err := a.tool.gitCmd().Output(ctx, "rev-parse", "--short=12", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// pruneFiles drops file digests of packages no record refers to any more.
func pruneFiles(s *state.State) {
	used := map[ids.ImportPath]bool{}
	for _, rec := range s.Tests {
		for p := range rec.Binary.Sources {
			used[p] = true
		}
	}
	for p := range s.Files {
		if !used[p] {
			delete(s.Files, p)
		}
	}
}
